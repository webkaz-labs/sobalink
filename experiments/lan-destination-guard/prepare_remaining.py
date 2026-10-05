"""Extend a fresh verified experiment copy; never change the application module."""
import hashlib
from pathlib import Path
import shutil

PINS = {
    'net/netcheck/standalone.go': '45cb0c976afdce3015d3552d35651e72c2faf1045ada6a29902c11e5734ab0aa',
    'derp/derphttp/derphttp_client.go': 'a6711c986564297a85621586ef7eb0acacd9a645989ffa4fbdd5ad29d420a531',
    'net/netcheck/netcheck.go': 'f0f907fde65f477137c67b7dd6841d7feb8bb224c42f8c1606e1e9f2b96fac08',
    'wgengine/magicsock/magicsock_linux.go': '3186c55c79581ad74299e61b875d422f36f280ceee189703b8acfb2cdd9ae211',
}

def verify(source):
    for name, digest in PINS.items():
        if hashlib.sha256((source/name).read_bytes()).hexdigest() != digest:
            raise SystemExit('pinned remaining-path source hash mismatch')

def insert(text, signature, body):
    if text.count(signature) != 1:
        raise SystemExit('unexpected pinned entrypoint structure')
    return text.replace(signature, signature+'\n'+body, 1)

def write(root, name, text):
    p=root/name; p.chmod(0o644); p.write_text(text)

def prepare(source, destination):
    verify(source)
    name='derp/derphttp/derphttp_client.go'
    text=(source/name).read_text()
    text=insert(text, 'type Client struct {', '''
    // Experiment only: immutable before use, exact numeric relay and injected writer.
    lanExperimentRelay netip.AddrPort
    lanExperimentDial func(context.Context, string, string) (net.Conn, error)
''')
    text=insert(text, 'func (c *Client) dialURL(ctx context.Context) (net.Conn, error) {', '''
    if lanExperimentStrict { return c.lanExperimentGuardedDial(ctx, "tcp", net.JoinHostPort(c.url.Hostname(), urlPort(c.url))) }
''')
    text=insert(text, 'func (c *Client) dialContext(ctx context.Context, proto, addr string) (net.Conn, error) {', '''
    if lanExperimentStrict { return c.lanExperimentGuardedDial(ctx, proto, addr) }
''')
    text=insert(text, 'func (c *Client) dialNodeUsingProxy(ctx context.Context, n *tailcfg.DERPNode, proxyURL *url.URL) (_ net.Conn, err error) {', '''
    if lanExperimentStrict { return nil, errLANExperimentDenied }
''')
    text+='''
// These variables and hooks exist only in the isolated experiment copy.
var lanExperimentStrict = true
var errLANExperimentDenied = errors.New("LAN experiment: TCP destination denied")
func (c *Client) lanExperimentGuardedDial(ctx context.Context, network, address string) (net.Conn, error) {
    ap, err := netip.ParseAddrPort(address)
    if err != nil || !ap.IsValid() || ap.Port() == 0 || ap.Addr().Zone() != "" || ap.Addr().Is4In6() || ap != c.lanExperimentRelay ||
       (network != "tcp" && network != "tcp4" && network != "tcp6") ||
       (network == "tcp4" && !ap.Addr().Is4()) || (network == "tcp6" && !ap.Addr().Is6()) { return nil, errLANExperimentDenied }
    // No hostname, DNS cache, environment proxy or alternative dialer is consulted.
    if c.lanExperimentDial != nil { return c.lanExperimentDial(ctx, network, address) }
    return netns.NewDialer(c.logf, c.netMon).DialContext(ctx, network, address)
}
'''
    write(destination,name,text)
    name='net/netcheck/netcheck.go';text=(source/name).read_text()
    for signature, body in [
        ('func (c *Client) runHTTPOnlyChecks(ctx context.Context, last *Report, rs *reportState, dm *tailcfg.DERPMap) error {', 'return errLANExperimentDisabled'),
        ('func (c *Client) measureHTTPSLatency(ctx context.Context, reg *tailcfg.DERPRegion) (time.Duration, netip.Addr, error) {', 'return 0, netip.Addr{}, errLANExperimentDisabled'),
        ('func (c *Client) measureAllICMPLatency(ctx context.Context, rs *reportState, need []*tailcfg.DERPRegion) error {', 'return errLANExperimentDisabled'),
        ('func (c *Client) measureICMPLatency(ctx context.Context, reg *tailcfg.DERPRegion, p *ping.Pinger) (_ time.Duration, ok bool, err error) {', 'return 0, false, errLANExperimentDisabled'),
    ]:
        text=insert(text,signature,'    if lanExperimentStrict { '+body+' }')
    marker='\t// The default lookup function if we don\'t set UseDNSCache is to use net.DefaultResolver.'
    if text.count(marker)!=1: raise SystemExit('unexpected DNS boundary')
    text=text.replace(marker,'\tif lanExperimentStrict { return zero, false } // No DNS fallback in experiment.\n'+marker,1)
    text+='\nvar lanExperimentStrict = true\nvar errLANExperimentDisabled = errors.New("LAN experiment: diagnostic disabled")\n'
    write(destination,name,text)
    name='net/netcheck/standalone.go';text=(source/name).read_text()
    text=insert(text,'func (c *Client) Standalone(ctx context.Context, bindAddr string) error {','    if lanExperimentStrict { return errLANExperimentDisabled }')
    write(destination,name,text)
    name='wgengine/magicsock/magicsock_linux.go';text=(source/name).read_text()
    text=insert(text,'func (c *Conn) listenRawDisco(family string) (io.Closer, error) {',
                '    if lanExperimentRawDisabled { return nil, errors.ErrUnsupported }')
    text+='\nvar lanExperimentRawDisabled = true\n';write(destination,name,text)
    here=Path(__file__).parent
    for package,fixture in [('derp/derphttp','derp_test.go.txt'),('net/netcheck','netcheck_test.go.txt')]:
        for test in (destination/package).glob('*_test.go'): test.unlink()
        shutil.copyfile(here/fixture,destination/package/'lan_remaining_experiment_test.go')
    shutil.copyfile(here/'raw_linux_test.go.txt',destination/'wgengine/magicsock/lan_raw_experiment_linux_test.go')
