package normalize

import (
	"math"
	"strings"
	"testing"

	"go.opentelemetry.io/collector/pdata/pcommon"
)

func putStr(m pcommon.Map, k, v string) {
	m.PutStr(k, v)
}

func putDouble(m pcommon.Map, k string, v float64) {
	m.PutDouble(k, v)
}

func putInt(m pcommon.Map, k string, v int64) {
	m.PutInt(k, v)
}

func TestAttrDoubleRejectsNonFiniteNegativeHuge(t *testing.T) {
	m := pcommon.NewMap()
	putStr(m, "c", "+Inf")
	if _, ok := attrDouble(m, "c"); ok {
		t.Error("string +Inf must be rejected")
	}
	putStr(m, "c", "NaN")
	if _, ok := attrDouble(m, "c"); ok {
		t.Error("string NaN must be rejected")
	}
	putDouble(m, "c", math.Inf(1))
	if _, ok := attrDouble(m, "c"); ok {
		t.Error("double +Inf must be rejected")
	}
	putDouble(m, "c", math.NaN())
	if _, ok := attrDouble(m, "c"); ok {
		t.Error("double NaN must be rejected")
	}
	putDouble(m, "c", -5.0)
	if _, ok := attrDouble(m, "c"); ok {
		t.Error("negative cost must be rejected")
	}
	putDouble(m, "c", 1e308)
	if _, ok := attrDouble(m, "c"); ok {
		t.Error("huge cost 1e308 must be rejected")
	}
	putDouble(m, "c", 0.00061761)
	if f, ok := attrDouble(m, "c"); !ok || f != 0.00061761 {
		t.Errorf("legit cost must pass, got %v ok=%v", f, ok)
	}
}

func TestAttrIntRejectsNegativeNonFiniteHuge(t *testing.T) {
	m := pcommon.NewMap()
	putInt(m, "t", -500)
	if _, ok := attrInt(m, "t"); ok {
		t.Error("negative tokens must be rejected")
	}
	putInt(m, "t", 9223372036854775807)
	if _, ok := attrInt(m, "t"); ok {
		t.Error("MaxInt64 tokens must be rejected (cap)")
	}
	putDouble(m, "t", math.NaN())
	if _, ok := attrInt(m, "t"); ok {
		t.Error("NaN tokens must be rejected")
	}
	putDouble(m, "t", math.Inf(1))
	if _, ok := attrInt(m, "t"); ok {
		t.Error("Inf tokens must be rejected")
	}
	putInt(m, "t", 8212)
	if n, ok := attrInt(m, "t"); !ok || n != 8212 {
		t.Errorf("legit tokens must pass, got %v ok=%v", n, ok)
	}
}

func TestFirstStringTruncates(t *testing.T) {
	m := pcommon.NewMap()
	long := strings.Repeat("m", 100*1024)
	putStr(m, "llm.model_name", long)
	if got := firstString(m, "llm.model_name"); len(got) != maxLabelLen {
		t.Errorf("model must truncate to %d, got %d", maxLabelLen, len(got))
	}
}
