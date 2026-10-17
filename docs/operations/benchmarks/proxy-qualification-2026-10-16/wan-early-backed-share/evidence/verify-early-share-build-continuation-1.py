#!/usr/bin/env python3
"""Read-only receipt guard for the fresh common early-share off/on pair."""
from pathlib import Path
import argparse
import importlib.util
import json
import re
ROOT = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location('early_share_build_proof', ROOT/'build-proof-continuation-1.py')
CHECK = importlib.util.module_from_spec(spec)
spec.loader.exec_module(CHECK)

def main():
    parser = argparse.ArgumentParser()
    for name in ['receipt','expected-receipt-sha256','server','agent','server-ref','agent-ref']:
        parser.add_argument('--'+name, required=True)
    args = parser.parse_args()
    CHECK.require(re.fullmatch('[0-9a-f]{64}', args.expected_receipt_sha256) and CHECK.sha(args.receipt)==args.expected_receipt_sha256, 'reviewed new build receipt differs')
    plan, receipt = CHECK.read(ROOT/'BUILD-CONTINUATION-1-PLAN.json'), CHECK.read(args.receipt)
    CHECK.verify_receipt(plan, receipt)
    for role in ['server','agent']:
        CHECK.require(str(Path(getattr(args,role)).resolve())==plan['binaries'][role] and getattr(args,role+'_ref')==plan['source_ref'], 'new runtime binary/source tuple differs')
    print(json.dumps({'accepted':True, 'diagnostic':'early-backed-share-build-tuple', 'source_ref':plan['source_ref'], 'reviewed_receipt_sha256':args.expected_receipt_sha256}))
if __name__ == '__main__':
    main()
