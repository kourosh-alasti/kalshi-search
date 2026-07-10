package learning

import "testing"

func TestTrainLogisticSeparates(t *testing.T) {
	examples := []trainingExample{
		{features: []string{"category:sports"}, label: 1},
		{features: []string{"category:sports"}, label: 1},
		{features: []string{"category:politics"}, label: 0},
		{features: []string{"category:politics"}, label: 0},
		{features: []string{"category:sports", "price_band:0-10"}, label: 1},
		{features: []string{"category:politics", "price_band:21-30"}, label: 0},
		{features: []string{"category:sports"}, label: 1},
		{features: []string{"category:politics"}, label: 0},
	}
	m := trainLogistic(examples, 100, 0.2)
	if m == nil {
		t.Fatal("nil model")
	}
	sports := m.predictFeatures([]string{"category:sports"})
	politics := m.predictFeatures([]string{"category:politics"})
	if sports <= politics {
		t.Fatalf("expected sports > politics, got %.3f vs %.3f", sports, politics)
	}
}

func TestSigmoid(t *testing.T) {
	if sigmoid(0) != 0.5 {
		t.Fatal("sigmoid(0) should be 0.5")
	}
}
