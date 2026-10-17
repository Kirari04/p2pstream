#!/usr/bin/env python3
"""Deferred exact documentary export; never run a validator/test/build/endpoint."""
from pathlib import Path
import argparse
import difflib
import hashlib
import json
import os
import re
import shutil
import stat
import subprocess
HERE=Path(__file__).resolve().parent
BASE=HERE.parents[1]
QUAL=BASE/'tmp/wan-route-reuse-qualification-20261017'
RUN=QUAL/'runs/original96-once'
CHECKS=BASE/'tmp/wan-route-reuse-checks-20261017'
BUILD=BASE/'tmp/wan-route-reuse-build-20261017'
SOURCE=BASE/'tmp/wan-route-reuse-source-20261017'
REVIEW=BASE/'tmp/wan-route-reuse-review-20261017'
DEST=BASE/'docs/operations/benchmarks/proxy-qualification-2026-10-16/wan-route-reuse'
BASE_REF='bd593420a6905f8922bb96d07ec351483c0df2c3'
GO_FILES=('internal/server/agent_transport_pool.go','internal/server/agent_transport_locality_test.go','internal/server/agent_transport_pool_test.go','internal/server/agent_real_transport_test.go','internal/server/agent_registry_test.go','internal/server/direct_transport_pool_test.go')
REVIEW_NAMES=('SOURCE-REVIEW.md','INPUTS.json','QUALIFICATION-SOURCE-REVIEW.md','BUILD-SOURCE-REVIEW.md','BUILD-RESULT-REVIEW.md','FINAL-QUALIFICATION-BINDING-REVIEW.md','OUTCOME-EXTRACTOR-SOURCE-REVIEW.md')
DENY_SUFFIXES=('.pem','.crt','.key','.db','.sqlite','.sqlite3','.test','.prof','.payload')

def require(value,message):
    if not value:raise ValueError(message)

def sha(path):return hashlib.sha256(Path(path).read_bytes()).hexdigest()

def read(path,limit=16<<20):
    path=Path(path);info=path.lstat()
    require(stat.S_ISREG(info.st_mode) and not path.is_symlink() and 0<info.st_size<=limit,'nonregular/unbounded metadata: '+str(path))
    return json.loads(path.read_text())

def dump(path,value):
    path.write_text(json.dumps(value,indent=2,sort_keys=True)+'\n')

def safe_file(path):
    info=path.lstat()
    require(stat.S_ISREG(info.st_mode) and not path.is_symlink(),'nonregular source: '+str(path))
    require(not path.name.endswith(DENY_SUFFIXES),'binary/cache/private/payload material is not exportable')

def main():
    parser=argparse.ArgumentParser()
    parser.add_argument('--export-approved-result',action='store_true',required=True)
    parser.parse_args()
    # This grant must be created by root AFTER the cohort and final analyses
    # stop writing. Merely importing/inspecting this file reads no run outputs.
    grant=read(HERE/'EXPORT-GRANT.json',1<<20)
    require(grant.get('root_approved') is True and grant.get('runtime_stopped') is True
            and grant.get('exporter_sha256')==sha(__file__),'root post-completion export grant missing')
    require(grant.get('classification') in ('complete qualification pass','complete timing rejection','incomplete or invalid qualification'),'explicit root outcome classification required')
    require(type(grant.get('outcome_files')) is list and len(grant['outcome_files'])>=2,'frozen final outcome and independent review bindings required')
    require(not DEST.exists(),'fresh packet destination required; no overwrite')
    require((HERE/'README.md').is_file(),'root-reviewed final outcome prose is not prepared')
    selected={};omitted=[]
    def choose(path,target,origin,expected=None):
        path=Path(path);safe_file(path)
        if expected is not None:require(sha(path)==expected,'frozen publication source changed: '+str(path))
        require(not Path(target).is_absolute() and '..' not in Path(target).parts,'unsafe documentary destination')
        binding=(target,origin,sha(path),path.stat().st_size)
        prior=selected.setdefault(path,binding)
        require(prior==binding,'conflicting or changed documentary mapping')
    # Canonical artifacts only; disposable workdirs carry readiness credentials,
    # endpoint logs and duplicated per-transfer output. Do not recurse into them.
    for path in sorted(QUAL.iterdir()):
        if path.is_file():choose(path,'evidence/'+path.name,'frozen qualification source, plan, grant or proof')
    for name in ('runner','provenance','contracts','offline-once','offline-continuation-1','execution-once'):
        directory=QUAL/name
        if directory.is_dir():
            for path in sorted(directory.rglob('*')):
                if path.is_file() and '__pycache__' not in path.parts:
                    choose(path,'evidence/'+str(path.relative_to(QUAL)),'reviewed harness, original/corrected offline histories or owned outer execution proof')
    require(RUN.is_dir(),'canonical original96 output is absent')
    for path in sorted(RUN.rglob('*')):
        if not path.is_file():continue
        relative=path.relative_to(RUN)
        if len(relative.parts)>1 or path.name.endswith(DENY_SUFFIXES) or path.name.endswith(('.ready.json','.stop')):
            omitted.append({'source':str(path),'sha256':sha(path),'bytes':path.stat().st_size,'disposition':'omit disposable nested workdir/TMP contents, TLS/cert/key files, endpoint logs/readiness, duplicates or payloads; canonical top-level evidence retained'})
            continue
        choose(path,'evidence/runs/original96-once/'+str(relative),'exact canonical rows/summary/attestation/lifecycle/defaults/statuses and durable wrapper logs')
    for directory,label in ((CHECKS,'focused'),(BUILD,'build')):
        for path in sorted(directory.rglob('*')):
            if path.is_file() and '__pycache__' not in path.parts:
                choose(path,'proofs/'+label+'/'+str(path.relative_to(directory)),'ordinary source/check/build plans and complete natural command/metadata proof; no executable bytes')
    choose(SOURCE/'SOURCE.json','source/SOURCE.json','six-file reviewed production patch inventory')
    source_inventory=read(SOURCE/'SOURCE.json')
    expected={row['path']:row['sha256'] for row in source_inventory['files']}
    require(set(expected)==set(GO_FILES),'six changed production/test source names differ')
    patch=[];base_rows=[]
    for name in GO_FILES:
        path=BASE/name;choose(path,'source/'+name,'exact reviewed compiled production/test file',expected[name])
        result=subprocess.run(['git','-C',str(BASE),'show',BASE_REF+':'+name],capture_output=True,timeout=5)
        require(len(result.stdout)<=2<<20 and len(result.stderr)<=65536,'base source read bound exceeded')
        if result.returncode!=0:
            require(name=='internal/server/agent_transport_locality_test.go','existing baseline source could not be resolved')
            before=b''
        else:before=result.stdout
        after=path.read_bytes()
        patch.extend(difflib.unified_diff(before.decode().splitlines(True),after.decode().splitlines(True),fromfile='a/'+name,tofile='b/'+name))
        base_rows.append({'path':name,'base_ref':BASE_REF,'base_sha256':hashlib.sha256(before).hexdigest() if before else None,'current_sha256':sha(path),'new_file':not before})
    # Explicit allowlist: later feasibility/next-candidate plans must not silently
    # enter a completed-result packet because they share a worker directory.
    for name in REVIEW_NAMES:choose(REVIEW/name,'analysis/independent-review/'+name,'frozen source/build/qualification review')
    for row in grant['outcome_files']:
        require(set(row)=={'source','destination','sha256'},'outcome binding shape differs')
        choose(Path(row['source']),row['destination'],'explicit root-frozen final outcome/independent review/audit',row['sha256'])
    for row in grant.get('additional_frozen_files',[]):
        require(set(row)=={'source','destination','sha256'},'additional binding shape differs')
        choose(Path(row['source']),row['destination'],'explicit root-selected frozen source/audit dependency',row['sha256'])
    choose(HERE/'README.md','README.md','root-reviewed final outcome explanation')
    choose(HERE/'EXPORT-GRANT.json','EXPORT-GRANT.json','post-completion documentary export authorization and frozen final input map')
    choose(HERE/'PUBLICATION-PLAN.json','PUBLICATION-PLAN.json','prospective source-only publication boundaries')
    choose(Path(__file__),'export.py','exact documentary exporter source')
    prior_lookup={};prior_packages=[]
    for package in ('current-production-wan','wan-controller-diagnostics','wan-controller-policy','wan-idle-policy','wan-early-backed-share'):
        parent=DEST.parent/package
        for name in ('README.md','SHA256SUMS','EXPORT-MAP.json','PRIOR-EVIDENCE.json'):
            path=parent/name
            if path.is_file():prior_packages.append({'path':'../'+package+'/'+name,'sha256':sha(path),'bytes':path.stat().st_size})
        mapping=parent/'EXPORT-MAP.json'
        if mapping.is_file():
            for row in read(mapping).get('copied_files',[]):
                path=parent/row['destination']
                if path.is_file() and not path.is_symlink() and sha(path)==row['sha256']:prior_lookup[(row['source'],row['sha256'])]={'path':'../'+package+'/'+row['destination'],'sha256':row['sha256']}
    DEST.mkdir();copied=[];canonical={};resolved={}
    for path,(target,origin,frozen_sha,frozen_bytes) in sorted(selected.items(),key=lambda item:(0 if item[1][0].startswith('source/') else 1,item[1][0],str(item[0]))):
        checksum,size=sha(path),path.stat().st_size
        require(checksum==frozen_sha and size==frozen_bytes,'selected input changed during export: '+str(path))
        identity=(checksum,size)
        if identity in canonical:target=canonical[identity]
        else:
            destination=DEST/target;destination.parent.mkdir(parents=True,exist_ok=True);shutil.copyfile(path,destination)
            require(sha(destination)==checksum,'documentary copy differs');canonical[identity]=target
        copied.append({'source':str(path),'destination':target,'sha256':checksum,'bytes':size,'origin':origin,'disposition':'exact bytes; identical selected content shares canonical destination'})
        resolved[(str(path),checksum)]={'path':target,'sha256':checksum}
    (DEST/'source/PRODUCTION.patch').write_text(''.join(patch))
    dump(DEST/'source/BASE-IDENTITY.json',{'base_commit':BASE_REF,'current_source_ref':read(QUAL/'provenance/stability-manifest.json')['source']['current_ref'],'files':base_rows,'patch_sha256':sha(DEST/'source/PRODUCTION.patch'),'full_compile_inventory':'proofs/focused/PLAN.json:source_files; complete430-file identity retained without copying the tree'})
    dispositions=[]
    compiled_paths={str(BASE/name) for name in read(CHECKS/'PLAN.json')['source_files']}
    for row in read(QUAL/'SOURCE.json')['files']:
        key=(row['path'],row['sha256'])
        if key in resolved:kind='copied exact';resolution=resolved[key]
        elif key in prior_lookup:kind='referenced exact published input';resolution=prior_lookup[key]
        elif Path(row['path']).name.endswith('.test'):kind='executable omitted';resolution={'reason':'exact binary path/hash/size and ordinary Go metadata/build receipt retained'}
        elif row['path'] in compiled_paths:kind='unchanged compilation input omitted';resolution={'reason':'base commit plus six exact files/patch; full430-file inventory and digest retained'}
        else:kind='inventory-only immutable prerequisite';resolution={'reason':'original frozen path/hash retained; published prior evidence or root-selected reference, no cache/archive/secret bytes copied'}
        dispositions.append({**row,'disposition':kind,'resolution':resolution})
    dump(DEST/'EXPORT-MAP.json',{'schema_version':1,'copied_files':copied,'exact_source_mapping_count':len(copied),'canonical_copied_file_count':len(canonical),'generated_files':['source/PRODUCTION.patch','source/BASE-IDENTITY.json','EXPORT-MAP.json','INPUT-DISPOSITION.json','PRIOR-EVIDENCE.json','CREDENTIAL-SCAN.json','MANIFEST.json','SHA256SUMS'],'no_tests_builds_validator_or_traffic':True})
    dump(DEST/'INPUT-DISPOSITION.json',{'source_manifest_entries':dispositions,'omitted_runtime_workdir_files':omitted,'omitted_categories':['binaries and module/build/temp caches','complete430-file source tree; six changed sources and exact patch/inventory retained','TLS cert/key contents and runtime credentials','endpoint readiness/application logs, disposable duplicate output and payloads','unrelated later plans or candidate work']})
    dump(DEST/'PRIOR-EVIDENCE.json',{'referenced_files':prior_packages,'historical_measurements_reused_as_controls':False,'original_legacy_source_overlay':'3ec137c3ad3d07028e867042fb00d3933912d7ab +43dbba91e6485b1fd5376c2331614adf8728f32d2aff0aea2ba08362dd0698bb'})
    # Never redact exact artifacts silently. Unexpected concrete run credentials
    # block export and require an explicit omission/reference decision by root.
    blocking=[];markers=[];pem=re.compile(rb'-----BEGIN (?:[A-Z ]*PRIVATE KEY|CERTIFICATE)-----')
    credentials=re.compile(rb'(?i)"(authorization|agent_token|password|client_secret|private_key)"\s*:\s*"([^"\r\n]+)"')
    for path in sorted(DEST.rglob('*')):
        if not path.is_file():continue
        data=path.read_bytes();relative=str(path.relative_to(DEST))
        if pem.search(data):blocking.append({'path':relative,'kind':'PEM/private/certificate material'})
        for match in credentials.finditer(data):
            field,value=match.group(1).lower(),match.group(2)
            harmless=(field==b'authorization' and value==b'bearer-present') or (path.suffix in ('.go','.py','.sh') and field==b'authorization' and value==b'Bearer ')
            if harmless:markers.append({'path':relative,'kind':'exact noncredential presence marker or source-only Bearer prefix'})
            elif path.suffix=='.go':markers.append({'path':relative,'kind':'review required: source fixture literal','field':field.decode(),'value_sha256':hashlib.sha256(value).hexdigest()})
            else:blocking.append({'path':relative,'kind':'concrete credential field','field':field.decode(),'value_sha256':hashlib.sha256(value).hexdigest()})
        for marker in (b'P2PSTREAM_QUALIFICATION_AGENT_TOKEN',b'Bearer ',b'.key.pem',b'private_key',b'password'):
            if marker in data:markers.append({'path':relative,'marker':marker.decode(),'count':data.count(marker),'kind':'source/path marker; review findings without printing values'})
    dump(DEST/'CREDENTIAL-SCAN.json',{'blocking_findings':blocking,'marker_findings':markers,'private_and_certificate_PEM_required_zero':True,'unexpected_concrete_runtime_credentials_block_export':True,'exact_bytes_redacted':False})
    require(not blocking,'credential scan requires explicit root disposition; partial draft is not publishable')
    files=[path for path in DEST.rglob('*') if path.is_file()]
    dump(DEST/'MANIFEST.json',{'schema_version':1,'classification':grant['classification'],'root_outcome_binding':sha(HERE/'EXPORT-GRANT.json'),'root_reviewed_README_sha256':sha(HERE/'README.md'),'exact_source_mappings':len(copied),'canonical_copied_files':len(canonical),'files_before_manifest_and_ledger':len(files),'bytes_before_manifest_and_ledger':sum(path.stat().st_size for path in files),'release_or_parity_clearance':False,'exporter_reexecuted_tests_builds_validators_or_traffic':False})
    with (DEST/'SHA256SUMS').open('x') as handle:
        for path in sorted(DEST.rglob('*')):
            if path.is_file() and path.name!='SHA256SUMS':handle.write(sha(path)+'  '+str(path.relative_to(DEST))+'\n')
    files=[path for path in DEST.rglob('*') if path.is_file()]
    print(json.dumps({'files':len(files),'bytes':sum(path.stat().st_size for path in files),'exact_source_mappings':len(copied),'manifest_sha256':sha(DEST/'MANIFEST.json'),'ledger_sha256':sha(DEST/'SHA256SUMS'),'blocking_credential_findings':len(blocking)}))
if __name__=='__main__':main()
