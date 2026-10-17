import json,re,sys
from pathlib import Path
(path,base,source_ref,overlay_sha,eof_sha,payload_sha,server_sha,agent_sha,
 server_build_id,agent_build_id,go_binary)=sys.argv[1:]
value=json.loads(Path(path).read_text())
def require(condition,message):
    if not condition: raise SystemExit(message)
require(value.get("schema_version")==1 and value.get("kind")=="p2pstream-transport-ceiling-build-bundle-v1","manifest identity")
source=value.get("source")
require(isinstance(source,dict) and source.get("base_commit")==base and source.get("build_tag")=="p2pstream_transport_ceiling" and source.get("source_ref")==source_ref and source.get("qualification_tag_enabled") is False,"manifest source")
overlay=source.get("overlay")
expected_files={
 "internal/agent/transport_ceiling_qualification_test.go":"35c19cc375ce646a98b0f7de86ac2e15fcec02e87ffbdeb7de1a54e3ab9cdf91",
 "internal/server/transport_ceiling_qualification_test.go":"4fe6edede3e1a9e8745ee4661d95a46d98bcba1e612c936df3d38c35b72459f6",
 "internal/tunnel/transport_ceiling_qualification_linux.go":"a941816b642e8c750f71811bf09eef86b626b885b3c64f00bd71d9aefdf6083d",
 "internal/tunnel/transport_ceiling_qualification_test.go":"ef6819426f61e4410d76a20ec6e2428be360acbe882961c624ff9274f4cbb64b"}
require(overlay=={"path":"transport-ceiling.patch","sha256":overlay_sha,"files":expected_files},"manifest overlay")
require(source.get("eof_correction_review")=={"path":"eof-correction-review.patch","sha256":eof_sha},"manifest EOF correction review")
require(value.get("payload")=={"algorithm":"byte((index*131+17)%251)","bytes":268435456,"period_bytes":1024,"sha256":payload_sha},"manifest payload")
toolchain=value.get("toolchain")
require(isinstance(toolchain,dict) and toolchain.get("go_binary")==go_binary and toolchain.get("go_version")=="go1.26.6" and toolchain.get("gotoolchain")=="local" and toolchain.get("gowork")=="off" and toolchain.get("goflags")=="" and toolchain.get("temporary_variables_on_owned_ext4") is True,"manifest toolchain")
expected={"server":("bin/transport-ceiling-server.test",server_sha,server_build_id),"agent":("bin/transport-ceiling-agent.test",agent_sha,agent_build_id)}
for role,(binary_path,digest,build_id) in expected.items():
    binary=value.get("binaries",{}).get(role)
    require(binary=={"path":binary_path,"sha256":digest,"build_id":build_id,"go_version":"go1.26.6","tags":["p2pstream_transport_ceiling"]},f"manifest {role} binary")
validation=value.get("validation")
require(isinstance(validation,dict) and validation.get("corrected_external_smoke_run") is False and validation.get("full_64_transfer_run") is False,"manifest pre-smoke state")
require(validation.get("agent_focused_tagged_normal",{}).get("count")==10 and validation["agent_focused_tagged_normal"].get("result")=="pass","manifest agent normal validation")
require(validation.get("agent_focused_tagged_race",{}).get("count")==5 and validation["agent_focused_tagged_race"].get("result")=="pass","manifest agent race validation")
carried=validation.get("carried_unchanged_helper_server",{})
require(carried.get("result")=="pass" and carried.get("prior_manifest_sha256")=="f5ab6117a1fb90992ba0efb9c57b3b85029358277bf31ee7a48c1c84207b9044" and carried.get("go_module_and_yamux_inputs_unchanged") is True,"manifest carried helper/server validation")
require(validation.get("agent_ordinary_tag_off_compile",{}).get("result")=="pass" and validation.get("patch_replay",{}).get("forward_exact") is True and validation["patch_replay"].get("reverse_exact") is True,"manifest compile/replay validation")
