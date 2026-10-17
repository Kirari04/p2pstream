#!/usr/bin/env python3
"""Validate the reviewed ordinary current server/aggregate-agent binary tuple."""
import argparse
import importlib.util
import json
from pathlib import Path
ROOT = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location('route_reuse_build_proof', ROOT/'build-proof.py')
CHECK = importlib.util.module_from_spec(spec); spec.loader.exec_module(CHECK)
def main():
    parser = argparse.ArgumentParser()
    for field in ['receipt', 'expected-receipt-sha256', 'server', 'server-ref', 'agent', 'agent-ref']:
        parser.add_argument('--'+field, required=True)
    args = parser.parse_args()
    plan = CHECK.read(ROOT/'PLAN.json')
    CHECK.require(CHECK.sha(args.receipt) == args.expected_receipt_sha256, 'reviewed receipt differs')
    receipt = CHECK.read(args.receipt)
    CHECK.verify_receipt(plan, receipt)
    for role in ['server', 'agent']:
        CHECK.require(getattr(args, role) == plan['binaries'][role] and getattr(args, role+'_ref') == plan['source_ref'], 'runtime endpoint tuple differs')
    print(json.dumps({'accepted':True, 'source_ref':plan['source_ref'], 'receipt_sha256':args.expected_receipt_sha256}))
if __name__ == '__main__': main()
