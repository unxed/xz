//go:build amd64 && !purego
// +build amd64,!purego

#include "textflag.h"

TEXT ·findBestMatch(SB), NOSPLIT, $0-104
	MOVQ $0, bestDist+88(FP)
	MOVQ $0, bestLen+96(FP)

	MOVQ dists_len+64(FP), CX
	TESTQ CX, CX
	JZ done_save

	MOVQ dict_base+0(FP), R8
	MOVQ dict_len+8(FP), R9
	MOVQ rear+24(FP), R10
	MOVQ data_base+32(FP), R11
	MOVQ data_len+40(FP), R12

	MOVQ dists_base+56(FP), R13

	XORQ R14, R14

	XORQ AX, AX

dists_loop:
	TESTQ CX, CX
	JZ done_save

	MOVQ (R13), DX
	ADDQ $8, R13
	DECQ CX

	MOVQ R10, BP
	SUBQ DX, BP
	ADDQ AX, BP

	MOVQ BP, SI
	ADDQ R9, SI
	CMPQ BP, $0
	CMOVQLT SI, BP

	MOVQ BP, SI
	SUBQ R9, SI
	CMPQ BP, R9
	CMOVQGE SI, BP

	MOVBQZX (R8)(BP*1), DI
	MOVBQZX (R11)(AX*1), R15
	CMPQ DI, R15
	JNE dists_loop

	XORQ BX, BX
	MOVQ R10, BP
	MOVQ -8(R13), DX
	SUBQ DX, BP
	CMPQ BP, $0
	JGE match_loop
	ADDQ R9, BP

match_loop:
	MOVQ R9, SI
	SUBQ BP, SI
	MOVQ R12, DI
	SUBQ BX, DI

	CMPQ SI, DI
	CMOVQGT DI, SI

	TESTQ SI, SI
	JZ match_done

inner_cmp8:
	CMPQ SI, $8
	JL inner_cmp1
	MOVQ (R8)(BP*1), DI
	MOVQ (R11)(BX*1), DX
	XORQ DI, DX
	JNZ inner_mismatch8
	ADDQ $8, BX
	ADDQ $8, BP
	SUBQ $8, SI
	JMP inner_cmp8

inner_mismatch8:
	BSFQ DX, DX
	SHRQ $3, DX
	ADDQ DX, BX
	ADDQ DX, BP
	JMP match_done

inner_cmp1:
	TESTQ SI, SI
	JZ wrap_check
	MOVBQZX (R8)(BP*1), DI
	MOVBQZX (R11)(BX*1), R15
	CMPQ DI, R15
	JNE match_done
	INCQ BX
	INCQ BP
	DECQ SI
	JMP inner_cmp1

wrap_check:
	CMPQ BP, R9
	JL match_loop
	XORQ BP, BP
	JMP match_loop

match_done:
	TESTQ BX, BX
	JZ dists_loop

	CMPQ BX, $1
	JNE check_best
	MOVQ -8(R13), DX
	DECQ DX
	MOVL rep0+80(FP), R15
	CMPQ DX, R15
	JNE dists_loop

check_best:
	CMPQ BX, AX
	JLE dists_loop

	MOVQ -8(R13), R14
	MOVQ BX, AX

	CMPQ AX, R12
	JE done_save

	JMP dists_loop

done_save:
	MOVQ R14, bestDist+88(FP)
	MOVQ AX, bestLen+96(FP)
	RET
