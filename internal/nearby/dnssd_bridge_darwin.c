//go:build darwin && cgo

#include "dnssd_bridge_darwin.h"
#include <dns_sd.h>
#include <dispatch/dispatch.h>
#include <pthread.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

/* No Go pointer or callback is retained by C. DNSService access is queue-only;
 * the mutex protects only bounded event storage/fences, never daemon IPC. */
enum { ST_CREATE=1, ST_SCHEDULE, ST_REGISTER, ST_QUERY, ST_CALLBACK, ST_ADMISSION };
enum { K_UNAVAILABLE=1, K_PERMISSION, K_CONFLICT, K_INVALID, K_UNSUPPORTED, K_BUDGET, K_CLOSED };
enum { RING_SIZE=64, MAX_CALLBACKS=512, MAX_RATE=16, MAX_BYTES=512*1024, MAX_QUERIES=193, MAX_CALLS=512 };
struct soba_dnssd_owner {
 dispatch_queue_t queue;
 pthread_mutex_t mutex;
 pthread_cond_t changed;
 int stopping;
 struct soba_dnssd_result failure;
 struct soba_dnssd_config config;
 struct soba_dnssd_event ring[RING_SIZE];
 unsigned head, count, callbacks, parsed, bytes, calls;
 time_t window;
 DNSServiceRef connection;
 DNSServiceRef queries[MAX_QUERIES];
 unsigned query_count;
 DNSRecordRef records[4];
};
static struct soba_dnssd_result result(int stage,int kind) { struct soba_dnssd_result r={stage,kind}; return r; }
static int error_kind(DNSServiceErrorType err) {
 switch(err) {
 case kDNSServiceErr_NoError:return 0;
 case kDNSServiceErr_NoAuth:case kDNSServiceErr_PolicyDenied:return K_PERMISSION;
 case kDNSServiceErr_NameConflict:case kDNSServiceErr_AlreadyRegistered:return K_CONFLICT;
 case kDNSServiceErr_BadParam:case kDNSServiceErr_BadFlags:case kDNSServiceErr_BadInterfaceIndex:return K_INVALID;
 case kDNSServiceErr_Unsupported:return K_UNSUPPORTED;
 default:return K_UNAVAILABLE;
 }
}
static int stopped(struct soba_dnssd_owner *o) {
 pthread_mutex_lock(&o->mutex);int stop=o->stopping||o->failure.kind;pthread_mutex_unlock(&o->mutex);return stop;
}
static void fail_locked(struct soba_dnssd_owner *o,int stage,int kind) {
 if(!o->failure.kind)o->failure=result(stage,kind);
 pthread_cond_broadcast(&o->changed);
}
static void fail(struct soba_dnssd_owner *o,int stage,int kind) {
 pthread_mutex_lock(&o->mutex);fail_locked(o,stage,kind);pthread_mutex_unlock(&o->mutex);
}
/* Every callback, including invalid/rejected ones, consumes lifetime work.
 * Rate excess stops admission before any native string/RDATA copy. */
static int admit_callback(struct soba_dnssd_owner *o) {
 struct timespec now;if(clock_gettime(CLOCK_MONOTONIC,&now)){fail(o,ST_ADMISSION,K_UNAVAILABLE);return 0;}
 pthread_mutex_lock(&o->mutex);
 if(o->stopping||o->failure.kind){pthread_mutex_unlock(&o->mutex);return 0;}
 if(++o->callbacks>=MAX_CALLBACKS){fail_locked(o,ST_ADMISSION,K_BUDGET);pthread_mutex_unlock(&o->mutex);return 0;}
 if(o->window!=now.tv_sec){o->window=now.tv_sec;o->parsed=0;}
 if(o->parsed>=MAX_RATE){fail_locked(o,ST_ADMISSION,K_BUDGET);pthread_mutex_unlock(&o->mutex);return 0;}
 o->parsed++;pthread_mutex_unlock(&o->mutex);return 1;
}
static void DNSSD_API register_reply(DNSServiceRef ref,DNSRecordRef record,DNSServiceFlags flags,DNSServiceErrorType err,void *context) {
 (void)ref;(void)record;(void)flags;struct soba_dnssd_owner *o=context;
 int admission=admit_callback(o);if(!admission)return;
 if(err)fail(o,ST_REGISTER,error_kind(err));
}
static void DNSSD_API query_reply(DNSServiceRef ref,DNSServiceFlags flags,uint32_t index,DNSServiceErrorType err,const char *name,uint16_t type,uint16_t class_,uint16_t length,const void *data,uint32_t ttl,void *context) {
 (void)ref;struct soba_dnssd_owner *o=context;
 int admission=admit_callback(o);if(!admission)return;
 /* Other callback fields are undefined on error. */
 if(err){fail(o,ST_CALLBACK,error_kind(err));return;}
 if(index==0||index!=o->config.interface_index||class_!=kDNSServiceClass_IN||length>SOBA_DNSSD_BYTES||!name||(!data&&length)){fail(o,ST_CALLBACK,K_INVALID);return;}
 if(type!=kDNSServiceType_PTR&&type!=kDNSServiceType_SRV&&type!=kDNSServiceType_TXT&&type!=kDNSServiceType_A){fail(o,ST_CALLBACK,K_INVALID);return;}
 size_t size=0;while(size<SOBA_DNSSD_NAME&&name[size])size++;
 if(!size||size==SOBA_DNSSD_NAME||size+length>SOBA_DNSSD_BYTES){fail(o,ST_CALLBACK,K_INVALID);return;}
 pthread_mutex_lock(&o->mutex);
 if(o->stopping||o->failure.kind){pthread_mutex_unlock(&o->mutex);return;}
 if(o->count==RING_SIZE||o->bytes>MAX_BYTES-size-length){fail_locked(o,ST_ADMISSION,K_BUDGET);pthread_mutex_unlock(&o->mutex);return;}
 o->bytes+=(unsigned)(size+length);
 struct soba_dnssd_event *e=&o->ring[(o->head+o->count)%RING_SIZE];
 memset(e,0,sizeof(*e));e->interface_index=index;e->ttl=ttl;e->type=type;e->class_=class_;e->length=length;e->removed=!(flags&kDNSServiceFlagsAdd)||ttl==0;
 memcpy(e->name,name,size);if(length)memcpy(e->data,data,length);
 o->count++;pthread_cond_signal(&o->changed);pthread_mutex_unlock(&o->mutex);
}
static int count_call(struct soba_dnssd_owner *o) {
 if(stopped(o))return K_CLOSED;
 if(o->calls>=MAX_CALLS){fail(o,ST_ADMISSION,K_BUDGET);return K_BUDGET;}
 o->calls++;return 0;
}
struct operation { struct soba_dnssd_owner *owner; const char *name; uint16_t type; struct soba_dnssd_result result; };
static void query_on_queue(void *context) {
 struct operation *op=context;struct soba_dnssd_owner *o=op->owner;
 int kind=count_call(o);if(kind){op->result=result(ST_QUERY,kind);return;}
 if(!o->connection||o->query_count>=MAX_QUERIES){op->result=result(ST_QUERY,K_BUDGET);return;}
 DNSServiceRef query=o->connection;
 DNSServiceErrorType err=DNSServiceQueryRecord(&query,kDNSServiceFlagsShareConnection,o->config.interface_index,op->name,op->type,kDNSServiceClass_IN,query_reply,o);
 /* On API failure query remains the shared input, not a new owned ref. */
 if(!err)o->queries[o->query_count++]=query;
 op->result=result(ST_QUERY,error_kind(err));
}
static void start_on_queue(void *context) {
 struct operation *op=context;struct soba_dnssd_owner *o=op->owner;
 int kind=count_call(o);if(kind){op->result=result(ST_CREATE,kind);return;}
 DNSServiceRef connection=NULL;
 DNSServiceErrorType err=DNSServiceCreateConnection(&connection);
 /* A failed call leaves its output uninitialized; only adopt success. */
 if(err){op->result=result(ST_CREATE,error_kind(err));return;}
 o->connection=connection;
 kind=count_call(o);if(kind){op->result=result(ST_SCHEDULE,kind);return;}
 err=DNSServiceSetDispatchQueue(o->connection,o->queue);
 if(err){op->result=result(ST_SCHEDULE,error_kind(err));return;}
 for(unsigned i=0;i<4;i++) {
  kind=count_call(o);if(kind){op->result=result(ST_REGISTER,kind);return;}
  const struct soba_dnssd_record *r=&o->config.records[i];
  DNSServiceFlags flags=r->type==kDNSServiceType_PTR?kDNSServiceFlagsShared:kDNSServiceFlagsUnique;
  err=DNSServiceRegisterRecord(o->connection,&o->records[i],flags,o->config.interface_index,r->name,r->type,kDNSServiceClass_IN,r->length,r->data,30,register_reply,o);
  if(err){op->result=result(ST_REGISTER,error_kind(err));return;}
 }
 struct operation query={o,o->config.records[0].name,kDNSServiceType_PTR,{0,0}};
 query_on_queue(&query);op->result=query.result;
}
struct soba_dnssd_owner *soba_dnssd_create(const struct soba_dnssd_config *cfg) {
 if(!cfg||!cfg->interface_index)return NULL;
 struct soba_dnssd_owner *o=calloc(1,sizeof(*o));if(!o)return NULL;
 if(pthread_mutex_init(&o->mutex,NULL)){free(o);return NULL;}
 if(pthread_cond_init(&o->changed,NULL)){pthread_mutex_destroy(&o->mutex);free(o);return NULL;}
 o->queue=dispatch_queue_create("sobalink.nearby.dnssd",DISPATCH_QUEUE_SERIAL);
 if(!o->queue){pthread_cond_destroy(&o->changed);pthread_mutex_destroy(&o->mutex);free(o);return NULL;}
 memcpy(&o->config,cfg,sizeof(*cfg));return o;
}
struct soba_dnssd_result soba_dnssd_start(struct soba_dnssd_owner *o) {
 struct operation op={o,NULL,0,{0,0}};dispatch_sync_f(o->queue,&op,start_on_queue);return op.result;
}
struct soba_dnssd_result soba_dnssd_query(struct soba_dnssd_owner *o,const char *name,uint16_t type) {
 struct operation op={o,name,type,{0,0}};dispatch_sync_f(o->queue,&op,query_on_queue);return op.result;
}
struct soba_dnssd_result soba_dnssd_next(struct soba_dnssd_owner *o,struct soba_dnssd_event *out) {
 pthread_mutex_lock(&o->mutex);
 while(!o->count&&!o->stopping&&!o->failure.kind)pthread_cond_wait(&o->changed,&o->mutex);
 struct soba_dnssd_result r=o->failure;
 if(!r.kind&&o->stopping)r=result(ST_CALLBACK,K_CLOSED);
 if(!r.kind){memcpy(out,&o->ring[o->head],sizeof(*out));o->head=(o->head+1)%RING_SIZE;o->count--;}
 pthread_mutex_unlock(&o->mutex);return r;
}
void soba_dnssd_stop(struct soba_dnssd_owner *o) {
 pthread_mutex_lock(&o->mutex);o->stopping=1;o->count=0;pthread_cond_broadcast(&o->changed);pthread_mutex_unlock(&o->mutex);
}
static void teardown_on_queue(void *context) {
 struct soba_dnssd_owner *o=context;
 /* Mandatory teardown is never blocked by admission/API budgets. One shared
  * deallocation invalidates every subordinate query and registered record. */
 if(o->connection){DNSServiceRefDeallocate(o->connection);o->connection=NULL;}
 o->query_count=0;memset(o->queries,0,sizeof(o->queries));memset(o->records,0,sizeof(o->records));
}
void soba_dnssd_join(struct soba_dnssd_owner *o) { dispatch_sync_f(o->queue,o,teardown_on_queue); }
void soba_dnssd_free(struct soba_dnssd_owner *o) {
 /* Caller has joined the queue AND all Start/Query/Next calls before release. */
 dispatch_release(o->queue);pthread_cond_destroy(&o->changed);pthread_mutex_destroy(&o->mutex);memset(o,0,sizeof(*o));free(o);
}
