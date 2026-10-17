#!/usr/bin/env python3
"""Emit a tuple only after complete current/legacy/source/runtime-grant checks."""
from pathlib import Path
import hashlib
import json
import os
import re
import stat
import subprocess
import sys
ROOT=Path(__file__).resolve().parent
BUILD=ROOT.parent/'wan-route-reuse-build-20261017'
LEGACY='3ec137c3ad3d07028e867042fb00d3933912d7ab'
OVERLAY='43dbba91e6485b1fd5376c2331614adf8728f32d2aff0aea2ba08362dd0698bb'
def require(value,message):
    if not value:raise ValueError(message)
def sha(path):return hashlib.sha256(Path(path).read_bytes()).hexdigest()
def read(path):
    path=Path(path);info=path.lstat()
    require(stat.S_ISREG(info.st_mode) and not path.is_symlink() and 0<info.st_size<=2<<20,'unbounded or nonregular provenance')
    return json.loads(path.read_text())
def verify(manifest_path):
    require(Path(manifest_path).resolve()==ROOT/'provenance/stability-manifest.json','unexpected runtime manifest')
    manifest=read(manifest_path);source=read(ROOT/'SOURCE.json')
    grant=read(ROOT/'RUNTIME-GRANT.json')
    require(grant=={'source_manifest_sha256':sha(ROOT/'SOURCE.json'),'runtime_approved':True,'maximum_transfers':96,'maximum_pairs':3},'exact original96 runtime grant missing')
    for row in source['files']:
        path=Path(row['path']);require(path.is_file() and not path.is_symlink() and sha(path)==row['sha256'],'frozen runtime/source input differs: '+str(path))
    for row in read(ROOT/'INPUTS.json')['files']:
        require(sha(row['path'])==row['sha256'],'original source/provenance input changed')
    expected=(ROOT/'REVIEWED-BUILD-RECEIPT-SHA256').read_text().strip()
    require(re.fullmatch('[0-9a-f]{64}',expected) and expected==manifest['current_build_receipt_sha256'],'reviewed production receipt differs')
    receipt_path=Path(manifest['current_build_receipt'])
    require(receipt_path==BUILD/'build-once/receipt.json' and sha(receipt_path)==expected,'current receipt path/hash differs')
    receipt=read(receipt_path);plan=read(BUILD/'PLAN.json')
    require(receipt['accepted'] is True and receipt['source_ref']==plan['source_ref']==manifest['source']['current_ref']
            and receipt['source_identity']['source_file_count']==430 and receipt['source_identity']==plan['source_identity'],
            'current source must identify full reviewed patched430-file compile closure')
    for role,key in [('server','current_server_binary'),('agent','current_agent_binary')]:
        require(manifest['binaries'][role]==receipt['binaries'][role]['path']==plan['binaries'][role]
                and manifest['sha256'][key]==receipt['binaries'][role]['sha256'],'new server/aggregate pair differs')
    argv=[sys.executable,'-I','-B',str(BUILD/'verify-build.py'),'--receipt',str(receipt_path),'--expected-receipt-sha256',expected]
    for role in ['server','agent']:argv+=['--'+role,manifest['binaries'][role],'--'+role+'-ref',manifest['source']['current_ref']]
    checked=subprocess.run(argv,capture_output=True,text=True,timeout=45,env={'PATH':'/usr/bin:/bin','PYTHONDONTWRITEBYTECODE':'1'})
    require(checked.returncode==0 and len(checked.stdout)<=16384 and len(checked.stderr)<=1<<20,'independent current build receipt guard rejected')
    require(json.loads(checked.stdout)=={'accepted':True,'source_ref':plan['source_ref'],'receipt_sha256':expected},'current build guard result differs')
    legacy=read(ROOT/'provenance/LEGACY-REUSE.json')
    require(manifest['source']['legacy_ref']==legacy['legacy_ref']==LEGACY
            and manifest['source']['legacy_overlay_sha256']==legacy['overlay_sha256']==OVERLAY
            and manifest['binaries']['legacy']==legacy['binary']['path']
            and manifest['sha256']['legacy_agent_binary']==legacy['binary']['sha256'],'exact compatible legacy identity differs')
    path=Path(legacy['binary']['path'])
    require(not path.is_symlink() and path.is_file() and os.access(path,os.X_OK)
            and path.stat().st_size==legacy['binary']['size'] and sha(path)==legacy['binary']['sha256'],'legacy executable changed')
    for name,key in [('runner/qualify-proxy-agent-growth-owned.sh','owned_runner'),('runner/run-owned-wan-stability.sh','stability_supervisor'),('runner/validate-owned-h2-stability.py','stability_validator'),('prove-default-transport.py','transport_defaults_proof')]:
        require(sha(ROOT/name)==manifest['sha256'][key],'new qualification source binding differs')
    return [manifest['binaries']['server'],manifest['binaries']['agent'],manifest['binaries']['legacy'],manifest['source']['current_ref'],manifest['source']['legacy_ref']]
def main():
    require(len(sys.argv)==2,'one reviewed runtime manifest required')
    values=verify(sys.argv[1])
    require(len(values)==5 and all(type(v) is str and v and '\n' not in v and '\r' not in v for v in values),'invalid tuple output')
    sys.stdout.write('\n'.join(values)+'\n')
if __name__=='__main__':main()
