//go:build amd64 && !purego
// +build amd64,!purego

#include "textflag.h"

TEXT ·prefetch(SB), NOSPLIT, $0-8
	MOVQ addr+0(FP), AX
	PREFETCHT0 (AX)
	RET
