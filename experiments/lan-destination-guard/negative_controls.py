#!/usr/bin/env python3
"""Mutation controls run fake-only entrypoints; never run native socket tests."""
import argparse
from pathlib import Path
import subprocess

CONTROLS = [
 ('wgengine/magicsock/rebinding_conn.go', 'func (c *RebindingUDPConn) checkLANDestination(addr netip.AddrPort) error {', 'return nil', './wgengine/magicsock', 'TestLANExperimentFakeEntryPoints'),
 ('derp/derphttp/derphttp_client.go', 'func (c *Client) lanExperimentGuardedDial(ctx context.Context, network, address string) (net.Conn, error) {', 'return c.lanExperimentDial(ctx, network, address)', './derp/derphttp', 'TestLANRemainingTCPFakeBoundaries'),
]

def main():
    p=argparse.ArgumentParser();p.add_argument('--engine',type=Path,required=True);args=p.parse_args()
    for filename,marker,body,package,test in CONTROLS:
        path=args.engine/filename;original=path.read_text()
        if original.count(marker)!=1:raise SystemExit('negative-control source mismatch')
        start=original.index(marker);end=original.index('\n}',start)+2
        try:
            path.write_text(original[:start]+marker+'\n '+body+'\n}'+original[end:])
            result=subprocess.run(['go','test','-race','-run','^'+test+'$',package],cwd=args.engine,text=True,stdout=subprocess.PIPE,stderr=subprocess.STDOUT,timeout=300)
            if result.returncode!=1 or '--- FAIL: '+test not in result.stdout:raise SystemExit('negative control did not fail the expected assertion')
            print('Guard removal failed the expected fake-writer boundary test: '+test)
        finally:path.write_text(original)
if __name__=='__main__':main()
