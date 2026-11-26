//go:build darwin && !pd_nofdscan

#include "textflag.h"

TEXT libc_proc_listallpids_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_proc_listallpids(SB)
GLOBL	·libc_proc_listallpids_trampoline_addr(SB), RODATA, $8
DATA	·libc_proc_listallpids_trampoline_addr(SB)/8, $libc_proc_listallpids_trampoline<>(SB)

TEXT libc_proc_pidinfo_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_proc_pidinfo(SB)
GLOBL	·libc_proc_pidinfo_trampoline_addr(SB), RODATA, $8
DATA	·libc_proc_pidinfo_trampoline_addr(SB)/8, $libc_proc_pidinfo_trampoline<>(SB)

TEXT libc_proc_pidfdinfo_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_proc_pidfdinfo(SB)
GLOBL	·libc_proc_pidfdinfo_trampoline_addr(SB), RODATA, $8
DATA	·libc_proc_pidfdinfo_trampoline_addr(SB)/8, $libc_proc_pidfdinfo_trampoline<>(SB)

TEXT libc_proc_pidfileportinfo_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_proc_pidfileportinfo(SB)
GLOBL	·libc_proc_pidfileportinfo_trampoline_addr(SB), RODATA, $8
DATA	·libc_proc_pidfileportinfo_trampoline_addr(SB)/8, $libc_proc_pidfileportinfo_trampoline<>(SB)
