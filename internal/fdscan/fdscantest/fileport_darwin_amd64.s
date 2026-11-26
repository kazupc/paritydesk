//go:build darwin && !pd_nofdscan

#include "textflag.h"

TEXT libc_fileport_makeport_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_fileport_makeport(SB)
GLOBL	·libc_fileport_makeport_trampoline_addr(SB), RODATA, $8
DATA	·libc_fileport_makeport_trampoline_addr(SB)/8, $libc_fileport_makeport_trampoline<>(SB)
