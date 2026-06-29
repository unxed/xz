//go:build amd64 && !purego
// +build amd64,!purego

#include "textflag.h"

TEXT ·getMatches(SB), NOSPLIT, $0-112
	MOVQ table_base+0(FP), R8
	MOVQ data_base+24(FP), R9
	MOVQ data_len+32(FP), R10
	MOVQ front+48(FP), R11
	MOVQ mask+56(FP), R12
	MOVQ hoff+64(FP), R13
	MOVQ h+72(FP), R14
	MOVQ positions_base+80(FP), R15
	MOVQ positions_len+88(FP), CX

	MOVQ $0, ret+104(FP)

	CMPQ R13, $0
	JL done
	TESTQ CX, CX
	JZ done

	MOVQ R13, AX
	INCQ AX
	TESTQ AX, AX
	JGE check_max
	XORQ AX, AX
check_max:
	CMPQ AX, R10
	CMOVQGT R10, AX

	MOVQ R13, BX
	INCQ BX
	SUBQ AX, BX

	MOVQ R11, BP
	SUBQ AX, BP

	CMPQ BP, $0
	JL skip_rear_sub
	SUBQ R10, BP
skip_rear_sub:

	MOVQ R14, SI
	ANDQ R12, SI

	MOVQ (R8)(SI*8), SI
	DECQ SI

	SUBQ BX, SI

	XORQ DI, DI

loop:
	CMPQ SI, $0
	JL done_save

	MOVQ BX, AX
	ADDQ SI, AX
	MOVQ AX, (R15)(DI*8)

	INCQ DI

	CMPQ DI, CX
	JGE done_save

	MOVQ BP, AX
	ADDQ SI, AX

	MOVQ AX, DX
	ADDQ R10, DX
	CMPQ AX, $0
	CMOVQLT DX, AX

	MOVLQZX (R9)(AX*4), AX

	TESTQ AX, AX
	JZ done_save

	SUBQ AX, SI

	JMP loop

done_save:
	MOVQ DI, ret+104(FP)
done:
	RET
