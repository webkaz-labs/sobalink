#ifndef SOBALINK_DNSSD_BRIDGE_H
#define SOBALINK_DNSSD_BRIDGE_H
#include <stdint.h>
#define SOBA_DNSSD_BYTES 1200
#define SOBA_DNSSD_NAME 256
struct soba_dnssd_record { char name[SOBA_DNSSD_NAME]; uint16_t type, length; unsigned char data[SOBA_DNSSD_BYTES]; };
struct soba_dnssd_config { uint32_t interface_index; struct soba_dnssd_record records[4]; };
struct soba_dnssd_event { uint32_t interface_index, ttl; uint16_t type, class_, length; int removed; char name[SOBA_DNSSD_NAME]; unsigned char data[SOBA_DNSSD_BYTES]; };
struct soba_dnssd_result { int stage, kind; };
struct soba_dnssd_owner;
struct soba_dnssd_owner *soba_dnssd_create(const struct soba_dnssd_config *);
struct soba_dnssd_result soba_dnssd_start(struct soba_dnssd_owner *);
struct soba_dnssd_result soba_dnssd_next(struct soba_dnssd_owner *, struct soba_dnssd_event *);
struct soba_dnssd_result soba_dnssd_query(struct soba_dnssd_owner *, const char *, uint16_t);
void soba_dnssd_stop(struct soba_dnssd_owner *);
void soba_dnssd_join(struct soba_dnssd_owner *);
void soba_dnssd_free(struct soba_dnssd_owner *);
#endif
