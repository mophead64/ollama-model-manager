package store

import (
	"errors"
	"testing"
)

func TestLabels(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()

	app, err := s.CreateLabel(ctx, "  Used by   Home Assistant ", "green")
	if err != nil {
		t.Fatal(err)
	}
	code, _ := s.CreateLabel(ctx, "Code", "blue")
	if _, err := s.CreateLabel(ctx, "code", "red"); !errors.Is(err, ErrLabelExists) {
		t.Errorf("a name differing only in case: %v, want ErrLabelExists", err)
	}
	if _, err := s.CreateLabel(ctx, "x", "teal"); !errors.Is(err, ErrLabelInvalid) {
		t.Errorf("a colour that isn't offered: %v", err)
	}
	if _, err := s.CreateLabel(ctx, "  ", "blue"); !errors.Is(err, ErrLabelInvalid) {
		t.Errorf("a blank name: %v", err)
	}

	if err := s.SetModelLabels(ctx, "qwen3:8b", []int64{app, code, 999}); err != nil {
		t.Fatal(err)
	}
	if err := s.LabelModels(ctx, code, []string{"Gemma3:4b", "llama3.2:3b"}, true); err != nil {
		t.Fatal(err)
	}
	if err := s.LabelModels(ctx, code, []string{"llama3.2:3b"}, false); err != nil {
		t.Fatal(err)
	}
	byModel, err := s.ModelLabels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := byModel["qwen3:8b"]; len(got) != 2 || got[0].Name != "Code" || got[1].Name != "Used by Home Assistant" || got[1].Color != "green" {
		t.Errorf("qwen3:8b's labels = %+v (sorted by name, the unknown ID ignored)", got)
	}
	if got := byModel["gemma3:4b"]; len(got) != 1 || got[0].ID != code {
		t.Errorf("gemma3:4b (labelled as Gemma3:4b) = %+v", got)
	}
	if _, ok := byModel["llama3.2:3b"]; ok {
		t.Error("llama3.2:3b's label was taken off again")
	}

	all, _ := s.Labels(ctx)
	n := len(BuiltinLabels)
	if len(all) != n+2 || !all[0].Builtin || all[n].Name != "Code" || all[n].Models != 2 || all[n+1].Models != 1 || all[n].Builtin {
		t.Errorf("labels = %+v (built-in first, then the user's by name)", all)
	}
	if err := s.DeleteLabel(ctx, all[0].ID); !errors.Is(err, ErrLabelBuiltin) {
		t.Errorf("deleting a built-in label: %v", err)
	}
	if _, err := s.UpdateLabel(ctx, all[0].ID, "Mine now", "blue"); !errors.Is(err, ErrLabelBuiltin) {
		t.Errorf("renaming a built-in label: %v", err)
	}
	if ok, err := s.UpdateLabel(ctx, app, "Home Assistant", "purple"); !ok || err != nil {
		t.Errorf("update: %v, %v", ok, err)
	}
	if _, err := s.UpdateLabel(ctx, app, "CODE", "purple"); !errors.Is(err, ErrLabelExists) {
		t.Errorf("renaming onto another label: %v", err)
	}
	if ok, _ := s.UpdateLabel(ctx, 999, "Nope", "blue"); ok {
		t.Error("updated a label that doesn't exist")
	}
	if err := s.DeleteLabel(ctx, code); err != nil {
		t.Fatal(err)
	}
	byModel, _ = s.ModelLabels(ctx)
	if got := byModel["qwen3:8b"]; len(got) != 1 || got[0].Name != "Home Assistant" {
		t.Errorf("after deleting Code, qwen3:8b = %+v", got)
	}
	if _, ok := byModel["gemma3:4b"]; ok {
		t.Error("deleting a label should take it off its models")
	}
}

func TestBuiltinLabelsOnce(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	// Opening the store again (every app start) doesn't add them twice.
	if err := addBuiltinLabels(ctx, s.db); err != nil {
		t.Fatal(err)
	}
	all, _ := s.Labels(ctx)
	if len(all) != len(BuiltinLabels) {
		t.Errorf("%d labels, want the %d built-in ones once", len(all), len(BuiltinLabels))
	}
	// Nor use up IDs doing so: the next label is the one after them.
	if id, _ := s.CreateLabel(ctx, "Mine", "blue"); id != int64(len(BuiltinLabels)+1) {
		t.Errorf("first label of the user's own got ID %d, want %d", id, len(BuiltinLabels)+1)
	}
}
