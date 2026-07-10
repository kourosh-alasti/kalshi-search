package learning

import (
	"math"
	"sort"
)

// logisticModel is a binary classifier over hashed categorical features.
type logisticModel struct {
	weights map[string]float64
	bias    float64
}

func trainLogistic(examples []trainingExample, epochs int, lr float64) *logisticModel {
	if len(examples) == 0 {
		return nil
	}
	m := &logisticModel{weights: map[string]float64{}}
	if epochs <= 0 {
		epochs = 80
	}
	if lr <= 0 {
		lr = 0.15
	}
	for ep := 0; ep < epochs; ep++ {
		for _, ex := range examples {
			p := m.predictFeatures(ex.features)
			err := p - ex.label
			m.bias -= lr * err
			for _, f := range ex.features {
				m.weights[f] -= lr * err
			}
		}
	}
	return m
}

func (m *logisticModel) predictFeatures(features []string) float64 {
	if m == nil {
		return 0.5
	}
	z := m.bias
	for _, f := range features {
		z += m.weights[f]
	}
	return sigmoid(z)
}

func sigmoid(z float64) float64 {
	if z > 20 {
		return 1
	}
	if z < -20 {
		return 0
	}
	return 1 / (1 + math.Exp(-z))
}

type weightedFeature struct {
	feature string
	weight  float64
}

func (m *logisticModel) topFeatures(features []string, n int) []weightedFeature {
	if m == nil || n <= 0 {
		return nil
	}
	var out []weightedFeature
	for _, f := range features {
		if w, ok := m.weights[f]; ok && w != 0 {
			out = append(out, weightedFeature{feature: f, weight: w})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return math.Abs(out[i].weight) > math.Abs(out[j].weight)
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}
