// Package testfixtures holds test data shared across packages: the golden
// vectors and the path to the frozen model. Only tests import it.
//
// It must not import internal/model: model's own tests import this package,
// and that would be an import cycle.
package testfixtures

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Golden vectors captured by training/golden.py, which scores these rows with
// the frozen artifact's weights. To regenerate after a (rare) model change:
//
//	cd training && source .venv/bin/activate.fish && python golden.py
//
// Shared by several tests: never mutate them.

// GoldenLegit is dataset row 0 (Class=0), in feature_order.
var GoldenLegit = []float64{
	0.0, -1.3598071336738, -0.0727811733098497, 2.53634673796914, 1.37815522427443,
	-0.338320769942518, 0.462387777762292, 0.239598554061257, 0.0986979012610507,
	0.363786969611213, 0.0907941719789316, -0.551599533260813, -0.617800855762348,
	-0.991389847235408, -0.311169353699879, 1.46817697209427, -0.470400525259478,
	0.207971241929242, 0.0257905801985591, 0.403992960255733, 0.251412098239705,
	-0.018306777944153, 0.277837575558899, -0.110473910188767, 0.0669280749146731,
	0.128539358273528, -0.189114843888824, 0.133558376740387, -0.0210530534538215,
	149.62,
}

const GoldenLegitScore = 0.26201699054165745

// GoldenFraud is dataset row 541 (Class=1), in feature_order.
var GoldenFraud = []float64{
	406.0, -2.3122265423263, 1.95199201064158, -1.60985073229769, 3.9979055875468,
	-0.522187864667764, -1.42654531920595, -2.53738730624579, 1.39165724829804,
	-2.77008927719433, -2.77227214465915, 3.20203320709635, -2.89990738849473,
	-0.595221881324605, -4.28925378244217, 0.389724120274487, -1.14074717980657,
	-2.83005567450437, -0.0168224681808257, 0.416955705037907, 0.126910559061474,
	0.517232370861764, -0.0350493686052974, -0.465211076182388, 0.320198198514526,
	0.0445191674731724, 0.177839798284401, 0.261145002567677, -0.143275874698919,
	0.0,
}

const GoldenFraudScore = 0.99999987775230792

// FrozenModelPath returns the path of the committed artifact, so tests run
// against the real file rather than a drift-prone copy.
func FrozenModelPath(t testing.TB) string {
	t.Helper()
	return filepath.Join(repoRoot(t), "models", "v1.0.0", "model.json")
}

// repoRoot walks up from this file until it finds go.mod.
func repoRoot(t testing.TB) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(thisFile)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found walking up from testfixtures")
		}
		dir = parent
	}
}
