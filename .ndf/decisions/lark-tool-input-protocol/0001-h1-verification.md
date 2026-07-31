# lark-tool-input-protocol H1/H2 Verification

## Summary

Hardened the currently registered Lark companion tools that can still confuse the model before any Feishu call is made:

- `lark_skill_read`: added a minimum valid input card, structured model-to-backend errors for missing or invalid `skill`, and a two-strike same-error stop-loss.
- `lark_inspect`: added minimum valid input cards for `connection` and `command` mode, structured model-to-backend errors for missing `mode` or command `argv`, and a two-strike same-error stop-loss.

`lark_connect` remains unchanged because it accepts only `{}` and does not carry ambiguous command arguments. Legacy Feishu tools such as `lark_create_doc`, `lark_send_message`, and `lark_read_bitable` remain unregistered and out of scope.

## Verification

- `go test ./internal/numind/biz/agent -run 'TestLarkCompanionToolsModelInputProtocolErrors' -count=1` - PASS
- `go test ./internal/numind/biz/agent -run 'TestLarkCompanionToolsModelInputProtocolErrors|TestLarkExecuteBPlusModelInputProtocolErrors|TestLarkPersonalWorkspace|TestPlatformToolFactory|TestPlatformFactory_NoLarkTools_WhenProviderAbsent|TestLarkTools_Metadata' -count=1` - PASS
- `go test ./internal/numind/biz/agent -run 'TestLangfuseLarkSkillReadRejectsUntrustedReferenceWithoutLoggingIt|TestLarkCompanionToolsModelInputProtocolErrors' -count=1` - PASS
- `go test ./internal/numind/biz/agent` - PASS
- `GOPROXY=https://goproxy.cn,direct PATH="$(go env GOPATH)/bin:$PATH" task lint` - PASS
- `go test ./...` - PASS

## Notes

The first implementation moved invalid `lark_skill_read` input rejection ahead of the existing safe Langfuse span. The broader agent package test caught that regression. The final implementation keeps the new structured protocol error while still creating a redacted `invalid`/`invalid` span, so observability remains intact without logging raw model-supplied skill or reference values.
