package proxy_test

import ()

// toolUseHistoryBody is a Claude Code style history: two resolved Bash calls
// (one errored), one resolved Read, and the user's follow-up. The next
// assistant turn is what the fake upstream streams back.
const toolUseHistoryBody = `{"model":"z-ai/glm-5.3","stream":true,"messages":[` +
	`{"role":"user","content":"run the tests"},` +
	`{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"go test ./..."}}]},` +
	`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","is_error":true,"content":"FAIL"}]},` +
	`{"role":"assistant","content":[{"type":"tool_use","id":"toolu_2","name":"Read","input":{"path":"a_test.go"}}]},` +
	`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_2","content":"package a"}]},` +
	`{"role":"assistant","content":[{"type":"tool_use","id":"toolu_3","name":"Bash","input":{"command":"go vet ./..."}}]},` +
	`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_3","content":"ok"}]}` +
	`]}`

const toolUseHistoryErrorCounts = `{"Bash":{"calls":2,"errors":1},"Read":{"calls":1,"errors":0}}`
