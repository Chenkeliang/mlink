package hermes

import (
	"bytes"
	"os/exec"
	"testing"
)

func TestHermesSendsUserBeforeModelAndPairsCompletion(t *testing.T) {
	r, err := PlanProvider("/home/test/.hermes", HTTPGrant{Endpoint: "http://127.0.0.1:8097", Token: "test"})
	if err != nil {
		t.Fatal(err)
	}
	script := `
import sys,types,queue,threading
base=types.ModuleType('agent.memory_provider');base.MemoryProvider=object
sys.modules['agent']=types.ModuleType('agent');sys.modules['agent.memory_provider']=base
ns={};exec(sys.stdin.read(),ns)
p=ns['MLinkMemoryProvider']();p._config={'endpoint':'http://127.0.0.1','token':'test'};p._platform='feishu';p._subject='union';p._session_id='session';p._queue=queue.Queue()
calls=[]
p._request=lambda path,payload,timeout: calls.append((path,payload)) or {'queued':True}
p.on_turn_start(1,'纠正：使用新接口')
assert len(calls)==1 and calls[0][0]=='/v1/turn-fragments'
assert calls[0][1]['role']=='user' and calls[0][1]['content']=='纠正：使用新接口'
p.sync_turn('纠正：使用新接口','已确认')
complete=p._queue.get_nowait()
assert complete['turn_id']==calls[0][1]['turn_id']
assert [m['role'] for m in complete['messages']]==['user','assistant']
assert not p._pending_user_turns
`
	cmd := exec.Command("python3", "-c", script)
	cmd.Stdin = bytes.NewReader(r[1].Content)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("adapter flow: %v %s", err, out)
	}
}
