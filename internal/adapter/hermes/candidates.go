package hermes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"mlink/internal/identity"
	"mlink/internal/install"
)

const maxCandidateOutput = 1 << 20

const candidateScript = `import json,sqlite3,sys
path=sys.argv[1]
limit=int(sys.argv[2])
db=sqlite3.connect("file:"+path+"?mode=ro",uri=True)
rows=db.execute("""select origin_json,coalesce(last_activity_at,started_at)
from sessions where source='feishu' and origin_json is not null and origin_json!=''
order by coalesce(last_activity_at,started_at) desc limit ?""",(limit,)).fetchall()
result=[]
for raw,last_seen in rows:
    try:
        origin=json.loads(raw)
    except (TypeError,ValueError):
        continue
    alternate=str(origin.get("user_id_alt") or "").strip()
    primary=str(origin.get("user_id") or "").strip()
    value=alternate or primary
    if not value:
        continue
    result.append({"display_name":str(origin.get("user_name") or ""),"kind":"union_id" if alternate else "user_id","value":value,"last_seen":int(last_seen or 0)})
print(json.dumps(result,separators=(",",":"),ensure_ascii=False))`

type candidateWire struct {
	DisplayName string `json:"display_name"`
	Kind        string `json:"kind"`
	Value       string `json:"value"`
	LastSeen    int64  `json:"last_seen"`
}

func DetectIdentityCandidates(ctx context.Context, runner install.CommandRunner, machine, hermesHome string, limit int) ([]identity.Candidate, error) {
	if runner == nil {
		runner = install.LocalTarget{}
	}
	if !orbMachinePattern.MatchString(machine) {
		return nil, errors.New("invalid Orb machine name")
	}
	home := filepath.Clean(hermesHome)
	if !filepath.IsAbs(home) || home == string(filepath.Separator) || limit < 1 || limit > 200 {
		return nil, errors.New("safe Hermes home and candidate limit are required")
	}
	args, err := candidateCommand(machine, filepath.Join(home, "state.db"), limit)
	if err != nil {
		return nil, err
	}
	output, err := runner.Run(ctx, args, nil)
	if err != nil {
		return nil, errors.New("detect Hermes identity candidates")
	}
	if len(output) > maxCandidateOutput {
		return nil, errors.New("Hermes candidate output exceeds size limit")
	}
	decoder := json.NewDecoder(io.LimitReader(bytes.NewReader(output), maxCandidateOutput+1))
	decoder.DisallowUnknownFields()
	var values []candidateWire
	if err := decoder.Decode(&values); err != nil {
		return nil, fmt.Errorf("decode Hermes identity candidates: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("Hermes candidate output contains multiple JSON values")
	}
	deduplicated := make(map[string]candidateWire, len(values))
	for _, value := range values {
		if value.Value == "" || value.Kind != "union_id" && value.Kind != "user_id" || value.LastSeen < 0 {
			return nil, errors.New("Hermes candidate output contains an invalid identity")
		}
		key := value.Kind + "\x00" + value.Value
		if previous, exists := deduplicated[key]; !exists || previous.LastSeen < value.LastSeen {
			deduplicated[key] = value
		}
	}
	result := make([]identity.Candidate, 0, len(deduplicated))
	for _, value := range deduplicated {
		result = append(result, identity.NewCandidate(value.DisplayName, value.Kind, value.Value, time.Unix(value.LastSeen, 0).UTC()))
	}
	sort.Slice(result, func(left, right int) bool { return result[left].LastSeen.After(result[right].LastSeen) })
	return result, nil
}

func candidateCommand(machine, databasePath string, limit int) ([]string, error) {
	path := filepath.Clean(databasePath)
	if !orbMachinePattern.MatchString(machine) || !filepath.IsAbs(path) || filepath.Base(path) != "state.db" || limit < 1 || limit > 200 {
		return nil, errors.New("invalid Hermes candidate command")
	}
	return []string{"orb", "-m", machine, "python3", "-c", candidateScript, path, strconv.Itoa(limit)}, nil
}
