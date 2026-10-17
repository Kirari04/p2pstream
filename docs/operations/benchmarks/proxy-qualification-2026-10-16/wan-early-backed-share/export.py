#!/usr/bin/env python3
"""Copy exact approved documentary inputs; never invoke a test or benchmark."""
from pathlib import Path
import hashlib
import json
import re
import shutil
import os
BASE=Path(__file__).resolve().parents[2]
ROOT=BASE/'tmp/wan-early-share-screen-20261017'
WORKER=BASE/'tmp/wan-early-share-source-20261017'
DEPS=BASE/'tmp/wan-early-share-dependencies-20261017'
DEST=BASE/'docs/operations/benchmarks/proxy-qualification-2026-10-16/wan-early-backed-share'
HERE=Path(__file__).resolve().parent
sha=lambda p:hashlib.sha256(Path(p).read_bytes()).hexdigest()
def dump(path,value):
 path.write_text(json.dumps(value,indent=2,sort_keys=True)+'\n')
def main():
 if DEST.exists():raise SystemExit('fresh package only')
 selected={}
 omitted=[]
 def choose(path,destination,origin):
  path=Path(path)
  if not path.is_file() or path.is_symlink():raise ValueError('nonregular input '+str(path))
  old=selected.setdefault(path,(destination,origin))
  if old!=(destination,origin):raise ValueError('ambiguous destination '+str(path))
 # New local scripts/plans/command proof histories and canonical arm artifacts.
 # Disposable run workdirs hold credentials and duplicate transfer output.
 for path in sorted(ROOT.rglob('*')):
  if not path.is_file():continue
  rel=path.relative_to(ROOT)
  if '__pycache__' in rel.parts or rel.parts[0] in ('test-source','test-source-continuation-1'):
   continue
  if any(part.startswith('proxy-agent-growth.') for part in rel.parts):
   omitted.append({'source':str(path),'sha256':sha(path),'bytes':path.stat().st_size,'disposition':'disposable workdir: duplicate transfer output, endpoint logs/readiness, TLS/keys or transient files; canonical rows/closure retained'})
   continue
  if path.name.endswith(('.release','.joined','.natural-exit')) or path.name=='server-app-ready.json':
   omitted.append({'source':str(path),'sha256':sha(path),'bytes':path.stat().st_size,'disposition':'transient sentinel/readiness marker; immutable scope/process proofs retained'})
   continue
  choose(path,'evidence/'+str(rel),'new source/plans, all failed and accepted continuations, canonical stopped run and audits')
 changes=json.loads((ROOT/'SOURCE-DERIVATIVE.json').read_text())['changes']
 changed={v['path'] for v in changes}|{'internal/tunnel/qualification_growth_trace_test.go'}
 for name in sorted(changed):
  choose(ROOT/'test-source-continuation-1'/name,'source/'+name,'exact changed/added Go source from compiled continuation; rest bound by baseline inventory')
  original=ROOT/'test-source'/name
  if original.exists():choose(original,'source-original/'+name,'preserved original focused attempt source; identical bytes deduplicated')
 for path in sorted(WORKER.rglob('*')):
  if not path.is_file() or '__pycache__' in path.parts or path.name=='PRODUCTION-RETENTION-NEXT.md':continue
  rel=path.relative_to(WORKER)
  if rel.parts[0]=='candidate':
   # Compiled canonical versions retained above; preserve source draft bindings
   # only through their exact inventories/diffs, avoiding sparse duplicate trees.
   continue
  choose(path,'proofs/focused/'+str(rel),'focused normal/tagged/race original failure, correction and accepted continuation proofs')
 for path in sorted(DEPS.iterdir()):
  if path.is_file():choose(path,'proofs/dependencies/'+path.name,'pinned dependency closure restoration source/proofs, no module/cache data')
 for path in sorted((DEPS/'graph-metadata-once').iterdir()):
  if path.is_file():choose(path,'proofs/dependencies/graph-metadata-once/'+path.name,'separate graph-metadata restoration after preserved first build failure')
 review_names={'SOURCE-REVIEW.md','INPUTS.json','FOCUSED-FAILURE-REVIEW.md','BUILD-SOURCE-REVIEW.md','BUILD-CONTINUATION-REVIEW.md','SIDECAR-SOURCE-REVIEW.md','FINAL-RUNTIME-BINDING-REVIEW.md','INCOMPLETE-OUTCOME-REVIEW.md','INCOMPLETE-OUTCOME-INPUTS.json'}
 for path in sorted((BASE/'tmp/wan-early-share-review-20261017').iterdir()):
  if path.is_file() and path.name in review_names:choose(path,'analysis/independent-review/'+path.name,'frozen independent source/build/proof/outcome reviews; future plans excluded')
 for name in ['INCOMPLETE-CORE-TRIAGE.md','INCOMPLETE-CORE-TRIAGE.json']:
  choose(BASE/'tmp/wan-early-share-outcome-source-20261017'/name,'analysis/source-triage/'+name,'frozen core/shutdown FIN source and raw-evidence analysis')
 choose(BASE/'tmp/wan-idle-policy-source-review-20261017/NEXT-POOL-SHARE-PLAN.md','analysis/NEXT-POOL-SHARE-PLAN.md','prospective exact fixed-candidate plan')
 for name in ['final-audit.py','incomplete-audit.py']:
  choose(BASE/'tmp/wan-early-share-final-audit-20261017'/name,'audit/'+name,'original complete-flow audit and separate authoritative incomplete closure audit source')
 choose(HERE/'README.md','README.md','new outcome explanation')
 choose(Path(__file__),'export.py','exact documentary exporter source')
 choose(HERE/'partial-export-scan-1/CREDENTIAL-SCAN.json','proofs/exporter/PRELIMINARY-CREDENTIAL-SCAN.json','preserved preliminary marker-only scan stop; exact bearer-present/source-prefix classification resolved without redaction')
 # Resolve unchanged dependencies against earlier exact published packages.
 refs=[]
 for package in ['wan-controller-diagnostics','wan-controller-policy','wan-idle-policy']:
  prior=DEST.parent/package
  for name in ['README.md','SHA256SUMS','EXPORT-MAP.json','PRIOR-EVIDENCE.json']:
   p=prior/name
   if p.is_file():refs.append({'path':'../'+package+'/'+name,'sha256':sha(p),'bytes':p.stat().st_size,'disposition':'immutable prerequisite evidence, referenced without recopy'})
 # Resolve every source-manifest input either to this package, previous mapped
 # published bytes, or an explicit intentional inventory-only category.
 prior_lookup={}
 for package in ['wan-controller-diagnostics','wan-controller-policy','wan-idle-policy']:
  prior=DEST.parent/package
  mp=prior/'EXPORT-MAP.json'
  if not mp.is_file():continue
  for row in json.loads(mp.read_text()).get('copied_files',[]):
   target=prior/row['destination']
   if target.is_file() and sha(target)==row['sha256']:
    prior_lookup[(row['source'],row['sha256'])]={'path':'../'+package+'/'+row['destination'],'sha256':row['sha256'],'bytes':target.stat().st_size}
 DEST.mkdir()
 copied=[];canonical={};selected_resolution={}
 for source,(relative,origin) in sorted(selected.items(),key=lambda row:(0 if row[1][0].startswith('source/') else 2 if row[1][0].startswith('source-original/') else 1,row[1][0],str(row[0]))):
  checksum=sha(source);size=source.stat().st_size
  key=(checksum,size)
  if key in canonical:relative=canonical[key]
  else:
   target=DEST/relative;target.parent.mkdir(parents=True,exist_ok=True);shutil.copyfile(source,target)
   if sha(target)!=checksum:raise ValueError('copy differs')
   canonical[key]=relative
  row={'source':str(source),'destination':relative,'sha256':checksum,'bytes':size,'origin':origin,'disposition':'exact bytes; identical selected content shares canonical destination'}
  copied.append(row);selected_resolution[(str(source),checksum)]={'package_path':relative,'sha256':checksum,'bytes':size}
 source=json.loads((ROOT/'SOURCE.json').read_text());dispositions=[];baseline=[]
 compiled=ROOT/'test-source-continuation-1'
 for row in source['files']:
  key=(row['path'],row['sha256']);path=Path(row['path'])
  if key in selected_resolution:kind='copied exact';resolution=selected_resolution[key]
  elif key in prior_lookup:kind='referenced exact published dependency';resolution=prior_lookup[key];baseline.append({'source':row['path'],**resolution})
  elif path.is_relative_to(ROOT/'test-source') or path.is_relative_to(compiled):
   rel=path.relative_to(ROOT/'test-source') if path.is_relative_to(ROOT/'test-source') else path.relative_to(compiled)
   canonical_source=compiled/rel
   selected_key=(str(canonical_source),row['sha256'])
   if selected_key in selected_resolution:kind='identical selected compiled source';resolution=selected_resolution[selected_key]
   else:kind='unchanged full-tree source omitted';resolution={'baseline':'../wan-controller-diagnostics/; exact462-file baseline inventory plus retained derivative diffs and both468-file inventories'}
  elif path.name.endswith('.test'):kind='binary omitted';resolution={'reason':'exact hash/size/Go metadata/build receipt retained; no executable bytes'}
  elif 'candidate' in path.parts:kind='sparse scratch duplicate omitted';resolution={'reason':'compiled changed source, exact core maps/diffs and full inventories retained'}
  else:kind='inventory-only prerequisite';resolution={'reason':'source/path/hash preserved in SOURCE and original immutable prerequisite evidence'}
  dispositions.append({**row,'disposition':kind,'resolution':resolution})
 dump(DEST/'EXPORT-MAP.json',{'schema_version':1,'copied_files':copied,'canonical_copied_file_count':len(canonical),'exact_source_mapping_count':len(copied),'generated_files':['EXPORT-MAP.json','INPUT-DISPOSITION.json','PRIOR-EVIDENCE.json','CREDENTIAL-SCAN.json','MANIFEST.json','SHA256SUMS'],'no_benchmark_test_build_or_validator_execution':True})
 dump(DEST/'INPUT-DISPOSITION.json',{'omitted_categories':['binary and module/build/temp cache bytes','both complete468-file compilation trees; changed compiled Go sources plus baseline/derivative inventories retained','disposable workdir credentials/TLS/cert/key files, endpoint logs/readiness, duplicate transfer output and request payloads','unrelated future production-retention/warm-lane plans'],'source_manifest_entries':dispositions,'omitted_runtime_workdir_files':omitted})
 dump(DEST/'PRIOR-EVIDENCE.json',{'referenced_files':refs,'resolved_source_manifest_references':baseline,'baseline_build_receipt_sha256':'d5115080339eea5a1881376c2d936b70122099be576a5c18866ef98c754d31bb','new_build_receipt_sha256':'0a13d33c752be01d2cabfc19db83354b8fdcafc0c9a8fa37af9347de6f989148','binaries_included':False,'historical_controls_reused_as_new_measurements':False})
 # Fail on actual PEM/private-key material or concrete credential-bearing raw
 # JSON fields. Source identifiers/CLI flags and omitted file paths are not secrets.
 scans=[];blocking=[]
 pem=re.compile(rb'-----BEGIN (?:[A-Z ]*PRIVATE KEY|CERTIFICATE)-----')
 credential=re.compile(rb'(?i)"(authorization|agent_token|password|client_secret|private_key)"\s*:\s*"([^"\r\n]+)"')
 for p in sorted(DEST.rglob('*')):
  if not p.is_file():continue
  data=p.read_bytes()
  if pem.search(data):blocking.append({'path':str(p.relative_to(DEST)),'kind':'PEM material'})
  for m in credential.finditer(data):
   # Source code and inventories can describe fixture keys; concrete runtime
   # credential values would require exclusion, never redaction of exact bytes.
   field,value=m.group(1).lower(),m.group(2)
   if field==b'authorization' and (value==b'bearer-present' or (p.relative_to(DEST).as_posix()=='evidence/runner.sh' and value==b'Bearer ')):
    scans.append({'path':str(p.relative_to(DEST)),'marker':m.group(1).decode(),'count':1,'classification':'exact noncredential bearer-present marker or source-only Bearer prefix; no credential value'})
   else:blocking.append({'path':str(p.relative_to(DEST)),'kind':'concrete credential JSON field','field':m.group(1).decode()})
  for marker in [b'P2PSTREAM_QUALIFICATION_AGENT_TOKEN',b'Bearer ',b'private_key',b'.key.pem',b'password']:
   count=data.count(marker)
   if count:scans.append({'path':str(p.relative_to(DEST)),'marker':marker.decode(),'count':count,'classification':'source identifier, request parser literal, or omitted credential-file path; no runtime value copied'})
 dump(DEST/'CREDENTIAL-SCAN.json',{'blocking_findings':blocking,'marker_findings':scans,'scope':'all selected exact artifact bytes and generated disposition/reference metadata','private_PEM_and_certificate_material':'zero required','concrete_runtime_credential_fields':'zero required','ephemeral_application_logs_readiness_TLS_keys':'excluded before copy','preliminary_marker_only_stop':'preserved in proofs/exporter/PRELIMINARY-CREDENTIAL-SCAN.json; bearer-present markers and the source-only Bearer prefix are explicitly classified, no artifact bytes redacted','test_build_validator_or_benchmark_execution':False})
 if blocking:raise SystemExit(json.dumps({'credential_scan_blocked':blocking}))
 files=sorted(p for p in DEST.rglob('*') if p.is_file())
 dump(DEST/'MANIFEST.json',{'schema_version':1,'classification':'INCOMPLETE: candidate strict raw closure/terminal attribution rejection; third control unstarted','comparative_effect_established':False,'original_qualification_cleared':False,'runtime_source_manifest_sha256':sha(ROOT/'SOURCE.json'),'incomplete_audit_sha256':sha(ROOT/'INCOMPLETE-AUDIT.json'),'authoritative_incomplete_audit_accepted':True,'exact_source_mappings':len(copied),'canonical_copied_files':len(canonical),'files_before_manifest_and_ledger':len(files),'bytes_before_manifest_and_ledger':sum(p.stat().st_size for p in files),'copied_artifact_bytes':sum((DEST/relative).stat().st_size for relative in canonical.values()),'all_files_in_final_ledger':True})
 with (DEST/'SHA256SUMS').open('x') as handle:
  for p in sorted(DEST.rglob('*')):
   if p.is_file() and p.name!='SHA256SUMS':handle.write(sha(p)+'  '+str(p.relative_to(DEST))+'\n')
 files=sorted(p for p in DEST.rglob('*') if p.is_file())
 print(json.dumps({'files':len(files),'bytes':sum(p.stat().st_size for p in files),'source_mappings':len(copied),'canonical_copied':len(canonical),'manifest_sha256':sha(DEST/'MANIFEST.json'),'ledger_sha256':sha(DEST/'SHA256SUMS'),'blocking_credentials':len(blocking),'marker_findings':len(scans)}))
if __name__=='__main__':main()
