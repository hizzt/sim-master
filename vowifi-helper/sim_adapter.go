package main

import (
	"errors"

	externalsim "github.com/1239t/swu-go/pkg/sim"
	enginesim "github.com/1239t/vowifi-go/engine/sim"
)

type simAdapter struct {
	inner *externalsim.DirectSIM
}

func (a *simAdapter) GetIMSI() (string, error) {
	return a.inner.GetIMSI()
}

func (a *simAdapter) CalculateAKA(rand16, autn16 []byte) (enginesim.AKAResult, error) {
	res, ck, ik, auts, err := a.inner.CalculateAKA(rand16, autn16)
	result := enginesim.AKAResult{RES: res, CK: ck, IK: ik, AUTS: auts}
	if errors.Is(err, externalsim.ErrSyncFailure) {
		return result, enginesim.ErrSyncFailure
	}
	return result, err
}

func (a *simAdapter) Close() error {
	return a.inner.Close()
}
