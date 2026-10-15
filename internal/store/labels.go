package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Labels are colour-coded tags for models ("used by app X", "for code"):
// built-in ones (BuiltinLabels), and the user's own, made on the Labels tab or
// while labelling a model. A model can have any number; they're kept by the
// model's name, so a model deleted and downloaded again gets its labels back.
const labelsSchema = `
CREATE TABLE IF NOT EXISTS labels (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT NOT NULL UNIQUE COLLATE NOCASE,
    color      TEXT NOT NULL,
    builtin    INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL
);

CREATE TABLE IF NOT EXISTS model_labels (
    model    TEXT NOT NULL COLLATE NOCASE,
    label_id INTEGER NOT NULL REFERENCES labels(id) ON DELETE CASCADE,
    PRIMARY KEY (model, label_id)
);
CREATE INDEX IF NOT EXISTS model_labels_label ON model_labels(label_id);
`

// LabelColors are the colours a label can have, in the order they're offered.
var LabelColors = []string{"blue", "green", "amber", "red", "purple", "gray"}

// LabelNameMax caps a label's name, in characters.
const LabelNameMax = 40

type Label struct {
	ID      int64
	Name    string
	Color   string // one of LabelColors
	Builtin bool   // one of BuiltinLabels: can't be renamed, recoloured or deleted
	Models  int    // how many models have it (Labels only)
}

// BuiltinLabels are there from the start, for the usual jobs a model does.
var BuiltinLabels = []Label{
	{Name: "Coding", Color: "blue"},
	{Name: "Chat", Color: "green"},
	{Name: "Vision", Color: "purple"},
	{Name: "Embeddings", Color: "gray"},
	{Name: "Agents", Color: "amber"},
	{Name: "Experimental", Color: "red"},
}

// addBuiltinLabels makes sure the built-in labels exist. One the user had
// already made with the same name is left as theirs.
func addBuiltinLabels(ctx context.Context, db *sql.DB) error {
	for _, l := range BuiltinLabels {
		// Not INSERT OR IGNORE: an ignored insert still uses up an ID, every start.
		if _, err := db.ExecContext(ctx, `INSERT INTO labels (name, color, builtin, created_at)
			SELECT ?, ?, 1, ? WHERE NOT EXISTS (SELECT 1 FROM labels WHERE name = ?)`,
			l.Name, l.Color, time.Now().UTC(), l.Name); err != nil {
			return err
		}
	}
	return nil
}

var (
	ErrLabelExists  = errors.New("there's already a label with that name")
	ErrLabelInvalid = errors.New("a label needs a name and a colour")
	ErrLabelBuiltin = errors.New("built-in labels can't be changed")
)

// cleanLabel checks and tidies a label's name and colour.
func cleanLabel(name, color string) (string, string, error) {
	name = strings.Join(strings.Fields(name), " ")
	if name == "" || !slices.Contains(LabelColors, color) {
		return "", "", ErrLabelInvalid
	}
	if len([]rune(name)) > LabelNameMax {
		return "", "", fmt.Errorf("a label's name can be at most %d characters", LabelNameMax)
	}
	return name, color, nil
}

// CreateLabel adds a label and returns its ID.
func (s *Store) CreateLabel(ctx context.Context, name, color string) (int64, error) {
	name, color, err := cleanLabel(name, color)
	if err != nil {
		return 0, err
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO labels (name, color, created_at) VALUES (?, ?, ?)`, name, color, time.Now().UTC())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return 0, ErrLabelExists
		}
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateLabel renames and recolours a label. ok is false if there's no such label.
func (s *Store) UpdateLabel(ctx context.Context, id int64, name, color string) (ok bool, err error) {
	name, color, err = cleanLabel(name, color)
	if err != nil {
		return false, err
	}
	if l, err := s.GetLabel(ctx, id); err != nil || l == nil {
		return false, err
	} else if l.Builtin {
		return false, ErrLabelBuiltin
	}
	res, err := s.db.ExecContext(ctx, `UPDATE labels SET name = ?, color = ? WHERE id = ?`, name, color, id)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return false, ErrLabelExists
		}
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// DeleteLabel removes a label, and takes it off every model. Built-in
// labels can't be deleted.
func (s *Store) DeleteLabel(ctx context.Context, id int64) error {
	if l, err := s.GetLabel(ctx, id); err != nil || l == nil {
		return err
	} else if l.Builtin {
		return ErrLabelBuiltin
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM labels WHERE id = ?`, id)
	return err
}

// GetLabel returns a label, or nil if there's no such label.
func (s *Store) GetLabel(ctx context.Context, id int64) (*Label, error) {
	var l Label
	err := s.db.QueryRowContext(ctx, `SELECT id, name, color, builtin FROM labels WHERE id = ?`, id).Scan(&l.ID, &l.Name, &l.Color, &l.Builtin)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &l, nil
}

// Labels returns every label, built-in ones first, then by name, with how
// many models have each.
func (s *Store) Labels(ctx context.Context) ([]Label, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT l.id, l.name, l.color, l.builtin, COUNT(ml.model)
		FROM labels l LEFT JOIN model_labels ml ON ml.label_id = l.id
		GROUP BY l.id ORDER BY l.builtin DESC, l.name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Label
	for rows.Next() {
		var l Label
		if err := rows.Scan(&l.ID, &l.Name, &l.Color, &l.Builtin, &l.Models); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// ModelLabels returns each labelled model's labels, by name, keyed by the
// model's name in lower case (names match whatever their case).
func (s *Store) ModelLabels(ctx context.Context) (map[string][]Label, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT ml.model, l.id, l.name, l.color, l.builtin FROM model_labels ml JOIN labels l ON l.id = ml.label_id
		ORDER BY l.name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]Label{}
	for rows.Next() {
		var model string
		var l Label
		if err := rows.Scan(&model, &l.ID, &l.Name, &l.Color, &l.Builtin); err != nil {
			return nil, err
		}
		key := strings.ToLower(model)
		out[key] = append(out[key], l)
	}
	return out, rows.Err()
}

// SetModelLabels gives a model exactly these labels (none, to clear them).
// IDs of labels that don't exist are ignored.
func (s *Store) SetModelLabels(ctx context.Context, model string, ids []int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM model_labels WHERE model = ?`, model); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO model_labels (model, label_id) SELECT ?, id FROM labels WHERE id = ?`, model, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// LabelModels adds a label to models (add), or takes it off them.
func (s *Store) LabelModels(ctx context.Context, id int64, models []string, add bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, m := range models {
		q := `DELETE FROM model_labels WHERE model = ? AND label_id = ?`
		if add {
			q = `INSERT OR IGNORE INTO model_labels (model, label_id) SELECT ?, id FROM labels WHERE id = ?`
		}
		if _, err := tx.ExecContext(ctx, q, m, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
