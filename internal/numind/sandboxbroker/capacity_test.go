package sandboxbroker

import (
	"errors"
	"testing"
	"time"
)

func TestCapacityP1UsesNearestRankOnBusinessSamples(t *testing.T) {
	values := make([]int64, 101)
	for index := range values {
		values[index] = gibibyte + int64(100-index)*mebibyte
	}
	if got := memAvailableP1(values); got != gibibyte+mebibyte {
		t.Fatalf("P1 = %d; want %d", got, gibibyte+mebibyte)
	}
}

func TestCapacityFormulaCapsFloorsAndDerivesThresholds(t *testing.T) {
	start := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	plan, err := CalculateSandboxCapacity(CapacityEvidence{
		Mode: CapacityEvidenceHistorical,
		Samples: capacitySamples(
			start,
			start.Add(7*24*time.Hour),
			169,
			8*gibibyte,
		),
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.BaselineBytes != 8*gibibyte {
		t.Fatalf("baseline = %d", plan.BaselineBytes)
	}
	if plan.ParentMaxBytes != 11*gibibyte/4 {
		t.Fatalf("parent max = %d", plan.ParentMaxBytes)
	}
	if plan.WorkloadMaxBytes != 9*gibibyte/4 {
		t.Fatalf("workload max = %d", plan.WorkloadMaxBytes)
	}
	if plan.WorkloadHighBytes != 2*gibibyte {
		t.Fatalf("workload high = %d", plan.WorkloadHighBytes)
	}
	if plan.WorkloadRecoveryBytes != plan.WorkloadMaxBytes*80/100 {
		t.Fatalf("workload recovery = %d", plan.WorkloadRecoveryBytes)
	}
	if plan.WorkloadShedBytes != plan.WorkloadMaxBytes*96/100 {
		t.Fatalf("workload shed = %d", plan.WorkloadShedBytes)
	}
	if plan.ParentMaxBytes%(64*mebibyte) != 0 {
		t.Fatalf("parent max %d is not floored to 64 MiB", plan.ParentMaxBytes)
	}
}

func TestCapacityFormulaUsesObservedBaselineBelowPOCCap(t *testing.T) {
	start := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	plan, err := CalculateSandboxCapacity(CapacityEvidence{
		Mode: CapacityEvidenceFreshSampling,
		Samples: capacitySamples(
			start,
			start.Add(72*time.Hour),
			73,
			7*gibibyte/2,
		),
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ParentMaxBytes != 9*gibibyte/4 {
		t.Fatalf("parent max = %d", plan.ParentMaxBytes)
	}
	if plan.WorkloadMaxBytes != 7*gibibyte/4 {
		t.Fatalf("workload max = %d", plan.WorkloadMaxBytes)
	}
	if plan.WorkloadHighBytes != plan.WorkloadMaxBytes*90/100 {
		t.Fatalf("workload high = %d", plan.WorkloadHighBytes)
	}
}

func TestCapacityBlocksInsufficientEvidenceAndMemory(t *testing.T) {
	start := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		evidence CapacityEvidence
		wantErr  error
	}{
		{
			name: "historical shorter than seven days",
			evidence: CapacityEvidence{
				Mode:    CapacityEvidenceHistorical,
				Samples: capacitySamples(start, start.Add(7*24*time.Hour-time.Second), 169, 8*gibibyte),
			},
			wantErr: ErrCapacityEvidenceInsufficient,
		},
		{
			name: "fresh sampling shorter than seventy two hours",
			evidence: CapacityEvidence{
				Mode:    CapacityEvidenceFreshSampling,
				Samples: capacitySamples(start, start.Add(72*time.Hour-time.Second), 73, 8*gibibyte),
			},
			wantErr: ErrCapacityEvidenceInsufficient,
		},
		{
			name: "historical mode cannot claim only seventy two hours",
			evidence: CapacityEvidence{
				Mode:    CapacityEvidenceHistorical,
				Samples: capacitySamples(start, start.Add(72*time.Hour), 73, 8*gibibyte),
			},
			wantErr: ErrCapacityEvidenceInsufficient,
		},
		{
			name: "machine leaves less than two GiB parent",
			evidence: CapacityEvidence{
				Mode:    CapacityEvidenceHistorical,
				Samples: capacitySamples(start, start.Add(7*24*time.Hour), 169, 3*gibibyte),
			},
			wantErr: ErrCapacityInsufficient,
		},
		{
			name: "sample gap larger than one hour",
			evidence: CapacityEvidence{
				Mode:    CapacityEvidenceHistorical,
				Samples: capacitySamples(start, start.Add(7*24*time.Hour), 168, 8*gibibyte),
			},
			wantErr: ErrCapacityEvidenceInsufficient,
		},
		{
			name: "no business-window samples",
			evidence: CapacityEvidence{
				Mode: CapacityEvidenceHistorical,
				Samples: []CapacitySample{
					{ObservedAt: start, MemAvailableBytes: 8 * gibibyte},
					{ObservedAt: start.Add(7 * 24 * time.Hour), MemAvailableBytes: 8 * gibibyte},
				},
			},
			wantErr: ErrCapacityEvidenceInsufficient,
		},
		{
			name: "unknown evidence mode",
			evidence: CapacityEvidence{
				Mode:    "guess",
				Samples: capacitySamples(start, start.Add(7*24*time.Hour), 169, 8*gibibyte),
			},
			wantErr: ErrCapacityEvidenceInvalid,
		},
		{
			name: "duplicate timestamp",
			evidence: CapacityEvidence{
				Mode: CapacityEvidenceHistorical,
				Samples: []CapacitySample{
					{ObservedAt: start, MemAvailableBytes: 8 * gibibyte, BusinessWindow: true},
					{ObservedAt: start, MemAvailableBytes: 8 * gibibyte, BusinessWindow: true},
				},
			},
			wantErr: ErrCapacityEvidenceInvalid,
		},
		{
			name: "nonpositive memory sample",
			evidence: CapacityEvidence{
				Mode: CapacityEvidenceHistorical,
				Samples: []CapacitySample{
					{ObservedAt: start, MemAvailableBytes: 0, BusinessWindow: true},
					{ObservedAt: start.Add(7 * 24 * time.Hour), MemAvailableBytes: 8 * gibibyte, BusinessWindow: true},
				},
			},
			wantErr: ErrCapacityEvidenceInvalid,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := CalculateSandboxCapacity(tt.evidence)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v; want %v", err, tt.wantErr)
			}
		})
	}
}

func TestCapacityAcceptsExactMinimumParentAndWorkload(t *testing.T) {
	start := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	plan, err := CalculateSandboxCapacity(CapacityEvidence{
		Mode: CapacityEvidenceHistorical,
		Samples: capacitySamples(
			start,
			start.Add(7*24*time.Hour),
			169,
			13*gibibyte/4,
		),
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ParentMaxBytes != 2*gibibyte ||
		plan.WorkloadMaxBytes != 3*gibibyte/2 {
		t.Fatalf("minimum boundary plan = %#v", plan)
	}
}

func capacitySamples(
	start time.Time,
	end time.Time,
	count int,
	memAvailableBytes int64,
) []CapacitySample {
	samples := make([]CapacitySample, count)
	for index := range samples {
		offset := time.Duration(0)
		if count > 1 {
			offset = time.Duration(
				int64(end.Sub(start)) * int64(index) / int64(count-1),
			)
		}
		samples[index] = CapacitySample{
			ObservedAt:        start.Add(offset),
			MemAvailableBytes: memAvailableBytes,
			BusinessWindow:    true,
		}
	}
	return samples
}
