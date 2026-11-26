/*
 * Where the libproc layout constants of internal/fdscan/layout.go come
 * from. Not built by CI; re-run before changing a constant or after a
 * macOS SDK update:
 *
 *   clang -arch arm64  -fsyntax-only scripts/dev/libproc-layout.c
 *   clang -arch x86_64 -fsyntax-only scripts/dev/libproc-layout.c
 *
 * Both must pass without output. Every value is little-endian on both
 * architectures.
 */
#include <stddef.h>
#include <sys/fcntl.h>
#include <sys/socket.h>
#include <sys/un.h>
#include <libproc.h>
#include <sys/proc_info.h>

#define ASSERT_EQ(expr, value) _Static_assert((expr) == (value), #expr " != " #value)

/* Lists: 8-byte entries. */
ASSERT_EQ(sizeof(struct proc_fdinfo), 8);                /* sizeFDInfo */
ASSERT_EQ(offsetof(struct proc_fdinfo, proc_fdtype), 4);
ASSERT_EQ(sizeof(struct proc_fileportinfo), 8);          /* sizeFileportInfo */
ASSERT_EQ(offsetof(struct proc_fileportinfo, proc_fdtype), 4);

/* struct proc_fileinfo and struct vinfo_stat. */
ASSERT_EQ(sizeof(struct proc_fileinfo), 24);
ASSERT_EQ(offsetof(struct proc_fileinfo, fi_openflags), 0); /* offOpenFlags */
ASSERT_EQ(sizeof(struct vinfo_stat), 136);
ASSERT_EQ(offsetof(struct vinfo_stat, vst_dev), 0);
ASSERT_EQ(offsetof(struct vinfo_stat, vst_ino), 8);
ASSERT_EQ(sizeof(struct vnode_info), 152);
ASSERT_EQ(sizeof(struct vnode_info_path), 1176);
ASSERT_EQ(offsetof(struct vnode_info_path, vip_path), 152);
ASSERT_EQ(MAXPATHLEN, 1024);                               /* maxPathLen */

/* PROC_PIDFDVNODEPATHINFO, PROC_PIDFILEPORTVNODEPATHINFO. */
ASSERT_EQ(sizeof(struct vnode_fdinfowithpath), 1200);      /* sizeVnodeFDPath */
ASSERT_EQ(offsetof(struct vnode_fdinfowithpath, pfi.fi_openflags), 0);
ASSERT_EQ(offsetof(struct vnode_fdinfowithpath, pvip), 24);
ASSERT_EQ(offsetof(struct vnode_fdinfowithpath, pvip.vip_vi.vi_stat.vst_dev), 24); /* offFDDev */
ASSERT_EQ(offsetof(struct vnode_fdinfowithpath, pvip.vip_vi.vi_stat.vst_ino), 32); /* offFDIno */
ASSERT_EQ(offsetof(struct vnode_fdinfowithpath, pvip.vip_path), 176);             /* offFDPath */

/* PROC_PIDVNODEPATHINFO. */
ASSERT_EQ(sizeof(struct proc_vnodepathinfo), 2352);        /* sizeVnodePathInfo */
ASSERT_EQ(offsetof(struct proc_vnodepathinfo, pvi_cdir.vip_vi.vi_stat.vst_dev), 0);    /* offCdirDev */
ASSERT_EQ(offsetof(struct proc_vnodepathinfo, pvi_cdir.vip_vi.vi_stat.vst_ino), 8);    /* offCdirIno */
ASSERT_EQ(offsetof(struct proc_vnodepathinfo, pvi_cdir.vip_path), 152);                /* offCdirPath */
ASSERT_EQ(offsetof(struct proc_vnodepathinfo, pvi_rdir), 1176);
ASSERT_EQ(offsetof(struct proc_vnodepathinfo, pvi_rdir.vip_vi.vi_stat.vst_dev), 1176); /* offRdirDev */
ASSERT_EQ(offsetof(struct proc_vnodepathinfo, pvi_rdir.vip_vi.vi_stat.vst_ino), 1184); /* offRdirIno */
ASSERT_EQ(offsetof(struct proc_vnodepathinfo, pvi_rdir.vip_path), 1328);               /* offRdirPath */

/* PROC_PIDREGIONPATHINFO (8) and the private PROC_PIDREGIONPATHINFO2 (22). */
ASSERT_EQ(sizeof(struct proc_regioninfo), 96);
ASSERT_EQ(offsetof(struct proc_regioninfo, pri_address), 80); /* offRegionAddr */
ASSERT_EQ(offsetof(struct proc_regioninfo, pri_size), 88);    /* offRegionSize */
ASSERT_EQ(sizeof(struct proc_regionwithpathinfo), 1272);      /* sizeRegionPath */
ASSERT_EQ(offsetof(struct proc_regionwithpathinfo, prp_vip), 96);
ASSERT_EQ(offsetof(struct proc_regionwithpathinfo, prp_vip.vip_vi.vi_stat.vst_dev), 96);  /* offRegionDev */
ASSERT_EQ(offsetof(struct proc_regionwithpathinfo, prp_vip.vip_vi.vi_stat.vst_ino), 104); /* offRegionIno */
ASSERT_EQ(offsetof(struct proc_regionwithpathinfo, prp_vip.vip_path), 248);               /* offRegionPath */

/* PROC_PIDTHREADPATHINFO. */
ASSERT_EQ(sizeof(struct proc_threadinfo), 112);
ASSERT_EQ(sizeof(struct proc_threadwithpathinfo), 1288);   /* sizeThreadPath */
ASSERT_EQ(offsetof(struct proc_threadwithpathinfo, pvip.vip_path), 264); /* offThreadPath */

/* PROC_PIDFDSOCKETINFO. */
ASSERT_EQ(sizeof(struct socket_fdinfo), 792);              /* sizeSocketInfo */
ASSERT_EQ(offsetof(struct socket_fdinfo, pfi.fi_openflags), 0);
ASSERT_EQ(offsetof(struct socket_fdinfo, psi.soi_family), 184); /* offSockFamily */
ASSERT_EQ(offsetof(struct socket_fdinfo, psi.soi_kind), 256);   /* offSockKind */
ASSERT_EQ(offsetof(struct socket_fdinfo, psi.soi_proto.pri_un.unsi_addr.ua_sun.sun_path), 282); /* offSockPath */
ASSERT_EQ(sizeof(((struct sockaddr_un *)0)->sun_path), 104);    /* sockPathLen */

/* Flavors and types. */
ASSERT_EQ(PROC_PIDLISTFDS, 1);
ASSERT_EQ(PROC_PIDTASKINFO, 4);
ASSERT_EQ(PROC_PIDLISTTHREADS, 6);
ASSERT_EQ(PROC_PIDREGIONPATHINFO, 8);
ASSERT_EQ(PROC_PIDVNODEPATHINFO, 9);
ASSERT_EQ(PROC_PIDTHREADPATHINFO, 10);
ASSERT_EQ(PROC_PIDLISTFILEPORTS, 14);
ASSERT_EQ(PROC_PIDFDVNODEPATHINFO, 2);
ASSERT_EQ(PROC_PIDFDSOCKETINFO, 3);
ASSERT_EQ(PROC_PIDFILEPORTVNODEPATHINFO, 2);
ASSERT_EQ(PROX_FDTYPE_VNODE, 1);
ASSERT_EQ(PROX_FDTYPE_SOCKET, 2);
ASSERT_EQ(AF_UNIX, 1);
ASSERT_EQ(SOCKINFO_UN, 3);
ASSERT_EQ(FREAD, 1);
ASSERT_EQ(FWRITE, 2);
/* PROC_PIDREGIONPATHINFO2 (22) is not in the public header. */
