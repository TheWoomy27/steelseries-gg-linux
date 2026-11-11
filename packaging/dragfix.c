// dragfix: lets files dragged out of GG (Electron) drop into other apps on
// Wayland.
//
// Chromium 138 offers a dragged file as text/uri-list but has no code to
// send it: a target asking for it gets nothing ("Cannot deliver data of
// type text/uri-list"), so Discord, browsers and file managers drop
// nothing. Chromium has its own copy of libwayland built in, so its
// functions cannot be wrapped; this watches GG's Wayland connection
// instead. Loaded into GG's Electron (LD_PRELOAD), it wraps recvmsg: when
// the compositor asks one of GG's data sources for text/uri-list
// (wl_data_source.send, which comes with the pipe to write to) while a
// drag of GG's is on, it keeps a copy of that pipe. Once Chromium has
// closed its end, if Chromium wrote nothing, it writes the file's URI
// itself. GG's drag handler (patched by gg-linux) writes the file being
// dragged to $XDG_RUNTIME_DIR/gg-linux-drag-<pid>.
//
// Build: cc -shared -fPIC -O2 -pthread -o libgg-dragfix.so dragfix.c -ldl

#define _GNU_SOURCE
#include <dlfcn.h>
#include <fcntl.h>
#include <pthread.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <sys/stat.h>
#include <sys/syscall.h>
#include <sys/un.h>
#include <time.h>
#include <unistd.h>

static ssize_t (*real_recvmsg)(int, struct msghdr *, int);
static ssize_t (*real_write)(int, const void *, size_t);

// A request for the dragged file that Chromium has yet to answer.
struct pending {
	int used;
	int fd;    // Chromium's end of the pipe
	int copy;  // ours
	dev_t dev; // the pipe, to tell Chromium's end from a reused number
	ino_t ino;
	int written; // Chromium wrote to it
	char *path;
};

#define MAX_PENDING 8
static struct pending pending[MAX_PENDING];
static int npending;
static pthread_mutex_t lock = PTHREAD_MUTEX_INITIALIZER;

__attribute__((constructor)) static void init(void) {
	real_recvmsg = (ssize_t(*)(int, struct msghdr *, int))dlsym(RTLD_NEXT, "recvmsg");
	real_write = (ssize_t(*)(int, const void *, size_t))dlsym(RTLD_NEXT, "write");
}

static int same_pipe(int fd, const struct pending *p) {
	struct stat st;
	return fstat(fd, &st) == 0 && st.st_dev == p->dev && st.st_ino == p->ino;
}

// dragged reads the file being dragged, if a drag of GG's is on.
static char *dragged(void) {
	const char *dir = getenv("XDG_RUNTIME_DIR");
	char marker[512];
	snprintf(marker, sizeof marker, "%s/gg-linux-drag-%d", dir ? dir : "/tmp", (int)getpid());
	FILE *f = fopen(marker, "re");
	if (!f)
		return NULL;
	char path[4096];
	size_t n = fread(path, 1, sizeof path - 1, f);
	fclose(f);
	path[n] = 0;
	while (n > 0 && (path[n - 1] == '\n' || path[n - 1] == '\r'))
		path[--n] = 0;
	return n > 0 && path[0] == '/' ? strdup(path) : NULL;
}

static void write_all(int fd, const char *s, size_t n) {
	while (n > 0) {
		ssize_t w = real_write(fd, s, n);
		if (w <= 0)
			return;
		s += w;
		n -= (size_t)w;
	}
}

static void send_uri(int fd, const char *path) {
	static const char hex[] = "0123456789ABCDEF";
	char out[16384];
	size_t o = strlen("file://");
	memcpy(out, "file://", o);
	for (const unsigned char *p = (const unsigned char *)path; *p && o + 4 < sizeof out; p++) {
		unsigned char c = *p;
		if ((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || strchr("/-_.~", c)) {
			out[o++] = (char)c;
		} else {
			out[o++] = '%';
			out[o++] = hex[c >> 4];
			out[o++] = hex[c & 15];
		}
	}
	out[o++] = '\r';
	out[o++] = '\n';
	write_all(fd, out, o);
}

static void finish(struct pending *p) {
	pthread_mutex_lock(&lock);
	int written = p->written;
	pthread_mutex_unlock(&lock);
	if (!written)
		send_uri(p->copy, p->path);
	// Electron has its own close(), which checks that fds it owns are
	// not closed behind its back; this one is ours.
	syscall(SYS_close, p->copy);
	free(p->path);
	pthread_mutex_lock(&lock);
	p->used = 0;
	__atomic_sub_fetch(&npending, 1, __ATOMIC_RELEASE);
	pthread_mutex_unlock(&lock);
}

// answer waits for Chromium to handle the request, then answers it if
// Chromium did not.
static void *answer(void *arg) {
	struct pending *p = arg;
	struct timespec tick = {0, 5 * 1000 * 1000};
	for (int i = 0; i < 1000 && same_pipe(p->fd, p); i++)
		nanosleep(&tick, NULL);
	finish(p);
	return NULL;
}

static void keep(int fd, char *path) {
	struct stat st;
	int copy = fcntl(fd, F_DUPFD_CLOEXEC, 3);
	if (copy < 0 || fstat(copy, &st) != 0) {
		if (copy >= 0)
			syscall(SYS_close, copy);
		free(path);
		return;
	}
	struct pending *p = NULL;
	pthread_mutex_lock(&lock);
	for (int i = 0; i < MAX_PENDING && !p; i++)
		if (!pending[i].used)
			p = &pending[i];
	if (p) {
		*p = (struct pending){.used = 1, .fd = fd, .copy = copy, .dev = st.st_dev, .ino = st.st_ino, .path = path};
		__atomic_add_fetch(&npending, 1, __ATOMIC_RELEASE);
	}
	pthread_mutex_unlock(&lock);
	if (!p) {
		syscall(SYS_close, copy);
		free(path);
		return;
	}
	pthread_t t;
	pthread_attr_t attr;
	pthread_attr_init(&attr);
	pthread_attr_setdetachstate(&attr, PTHREAD_CREATE_DETACHED);
	if (pthread_create(&t, &attr, answer, p) != 0)
		finish(p);
	pthread_attr_destroy(&attr);
}

static int write_pipe(int fd) {
	struct stat st;
	return fstat(fd, &st) == 0 && S_ISFIFO(st.st_mode) && (fcntl(fd, F_GETFL) & O_ACCMODE) == O_WRONLY;
}

static int is_wayland(int sock) {
	struct sockaddr_un a;
	socklen_t len = sizeof a;
	memset(&a, 0, sizeof a);
	if (getpeername(sock, (struct sockaddr *)&a, &len) != 0 || a.sun_family != AF_UNIX)
		return 0;
	a.sun_path[sizeof a.sun_path - 1] = 0;
	const char *base = strrchr(a.sun_path, '/');
	base = base ? base + 1 : a.sun_path;
	const char *display = getenv("WAYLAND_DISPLAY");
	if (display && strrchr(display, '/'))
		display = strrchr(display, '/') + 1;
	return strncmp(base, "wayland-", 8) == 0 || (display && strcmp(base, display) == 0);
}

#define MAX_FDS 16

// inspect looks through what GG read from the compositor for requests
// for text/uri-list. A wl_data_source.send event is opcode 1 with one
// string, the MIME type; its pipe comes with it, and pipes come in the
// order of the events that carry them.
static void inspect(int sock, struct msghdr *msg, size_t n) {
	int pipes[MAX_FDS], npipes = 0;
	for (struct cmsghdr *c = CMSG_FIRSTHDR(msg); c; c = CMSG_NXTHDR(msg, c)) {
		if (c->cmsg_level != SOL_SOCKET || c->cmsg_type != SCM_RIGHTS)
			continue;
		size_t count = (c->cmsg_len - CMSG_LEN(0)) / sizeof(int);
		for (size_t i = 0; i < count; i++) {
			int fd;
			memcpy(&fd, CMSG_DATA(c) + i * sizeof(int), sizeof fd);
			if (!write_pipe(fd))
				continue;
			if (npipes == MAX_FDS)
				return;
			pipes[npipes++] = fd;
		}
	}
	if (npipes == 0 || !is_wayland(sock))
		return;

	unsigned char *b = malloc(n);
	if (!b)
		return;
	size_t got = 0;
	for (size_t i = 0; i < (size_t)msg->msg_iovlen && got < n; i++) {
		size_t take = msg->msg_iov[i].iov_len < n - got ? msg->msg_iov[i].iov_len : n - got;
		memcpy(b + got, msg->msg_iov[i].iov_base, take);
		got += take;
	}
	int uri[MAX_FDS], nsends = 0;
	for (size_t off = 0; off + 8 <= got;) {
		uint32_t word, len;
		memcpy(&word, b + off + 4, 4);
		size_t size = word >> 16, op = word & 0xffff;
		if (size < 8 || size % 4 || off + size > got)
			break;
		if (op == 1 && size >= 16) {
			memcpy(&len, b + off + 8, 4);
			const char *s = (const char *)b + off + 12;
			if (len >= 2 && len <= 256 && 12 + ((len + 3) & ~3u) == size && s[len - 1] == 0 &&
			    strlen(s) == len - 1 && strchr(s, '/')) {
				if (nsends == MAX_FDS)
					break;
				uri[nsends++] = strcmp(s, "text/uri-list") == 0;
			}
		}
		off += size;
	}
	free(b);
	// Only when every pipe has its event: otherwise which is which is
	// not known.
	if (nsends != npipes)
		return;
	for (int i = 0; i < nsends; i++) {
		if (!uri[i])
			continue;
		char *path = dragged();
		if (path)
			keep(pipes[i], path);
	}
}

ssize_t recvmsg(int fd, struct msghdr *msg, int flags) {
	if (!real_recvmsg)
		real_recvmsg = (ssize_t(*)(int, struct msghdr *, int))dlsym(RTLD_NEXT, "recvmsg");
	ssize_t n = real_recvmsg(fd, msg, flags);
	if (n > 0 && !(flags & MSG_PEEK) && msg && msg->msg_control && msg->msg_controllen >= sizeof(struct cmsghdr))
		inspect(fd, msg, (size_t)n);
	return n;
}

ssize_t write(int fd, const void *buf, size_t count) {
	if (!real_write)
		real_write = (ssize_t(*)(int, const void *, size_t))dlsym(RTLD_NEXT, "write");
	if (count > 0 && __atomic_load_n(&npending, __ATOMIC_ACQUIRE) > 0) {
		pthread_mutex_lock(&lock);
		for (int i = 0; i < MAX_PENDING; i++)
			if (pending[i].used && pending[i].fd == fd && same_pipe(fd, &pending[i]))
				pending[i].written = 1;
		pthread_mutex_unlock(&lock);
	}
	return real_write(fd, buf, count);
}
