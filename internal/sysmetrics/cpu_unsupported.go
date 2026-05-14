//go:build !linux

package sysmetrics

func NewProcessCPUSampler() *ProcessCPUSampler {
	return &ProcessCPUSampler{}
}

func (s *ProcessCPUSampler) Sample() (float64, bool, error) {
	return 0, false, nil
}
