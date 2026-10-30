package reportsite

import "testing"

func TestParseResult(t *testing.T) {
	good := resultJSON(haiku, [6]float64{1, 1, 1, 1, 0.75, 1}, [6]float64{0.4, 0.0833, 0.2818, 0, 0.25, 1})
	tests := []struct {
		name    string
		data    string
		agent   string
		wantErr bool
		dims    int
		first   string
		withPct int
	}{
		{"six dimensions and overall", string(good), "claude-code", false, 7, "Accuracy", 100},
		{"only agent is used when the name differs", string(good), "codex", false, 7, "Accuracy", 100},
		{"not json", "{nope", "claude-code", true, 0, "", 0},
		{"no agents", `{"agents":{}}`, "claude-code", true, 0, "", 0},
		{"no lift block", `{"agents":{"claude-code":{}}}`, "claude-code", true, 0, "", 0},
		{"malformed dimension", `{"agents":{"claude-code":{"lift":{"accuracy":"x"}}}}`, "claude-code", true, 0, "", 0},
		{"unscored dimensions are left out", `{"agents":{"claude-code":{"lift":{"security":{"with_skill":1.5,"without_skill":-1}}}}}`, "claude-code", false, 1, "Security", 100},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := parseResult([]byte(tt.data), tt.agent)
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil {
				return
			}
			if len(res.Dimensions) != tt.dims || res.Dimensions[0].Label != tt.first || res.Dimensions[0].WithPct != tt.withPct {
				t.Errorf("dimensions = %+v", res.Dimensions)
			}
		})
	}
}

func TestParseResultOrderAndOverall(t *testing.T) {
	res, err := parseResult(resultJSON(haiku, [6]float64{1, 1, 1, 1, 0.75, 1}, [6]float64{0.4, 0.0833, 0.2818, 0, 0.25, 1}), "claude-code")
	if err != nil {
		t.Fatal(err)
	}
	var labels []string
	for _, d := range res.Dimensions {
		labels = append(labels, d.Label)
	}
	want := []string{"Accuracy", "Goal accuracy", "Behavior check", "Skill execution", "Skill efficiency", "Security", "Overall"}
	if len(labels) != len(want) {
		t.Fatalf("labels = %v", labels)
	}
	for i := range want {
		if labels[i] != want[i] {
			t.Errorf("dimension %d = %q, want %q", i, labels[i], want[i])
		}
	}
	if last := res.Dimensions[6]; !last.Overall || last.With != 0.95 {
		t.Errorf("overall row = %+v", last)
	}
	if res.Activation.Status != "activated" || res.Activation.Activated != 3 {
		t.Errorf("activation = %+v", res.Activation)
	}
}
