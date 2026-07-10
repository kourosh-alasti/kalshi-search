package learning

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
)

type trainingExample struct {
	features []string
	label    float64 // 1 = took/position, 0 = pass
}

// LoadTrainingExamples returns labeled examples from suggestion_records for one user.
func LoadTrainingExamples(ctx context.Context, db *sql.DB, phone string, logger *slog.Logger) ([]trainingExample, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT s.features, l.label
		FROM suggestion_records s
		INNER JOIN suggestion_records l
			ON s.phone = l.phone AND s.suggestion_id = l.suggestion_id AND l.kind = 'label'
		WHERE s.phone = ? AND s.kind = 'suggestion'
			AND l.label IN ('took', 'pass', 'position')`,
		phone)
	if err != nil {
		return nil, fmt.Errorf("loading training examples: %w", err)
	}
	defer rows.Close()

	var out []trainingExample
	for rows.Next() {
		var featuresJSON, label string
		if err := rows.Scan(&featuresJSON, &label); err != nil {
			return nil, err
		}
		var feats []string
		if featuresJSON != "" {
			if err := json.Unmarshal([]byte(featuresJSON), &feats); err != nil {
				if logger != nil {
					logger.Warn("skipping training example with invalid features JSON",
						"phone", phone, "label", label, "error", err)
				}
				continue
			}
		}
		ex := trainingExample{features: feats}
		switch label {
		case "took", "position":
			ex.label = 1
		case "pass":
			ex.label = 0
		default:
			continue
		}
		out = append(out, ex)
	}
	return out, rows.Err()
}
