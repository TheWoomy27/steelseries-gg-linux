// dragfix: lets files dragged out of GG (Electron) drop into other apps on
// Wayland.
//
// Chromium 138 offers a dragged file as text/uri-list but has no code to
// send it: a target asking for it gets nothing ("Cannot deliver data of
// type text/uri-list"), so Discord, browsers and file managers drop
// nothing. Loaded into GG's Electron (LD_PRELOAD), this wraps the
// listener of each Wayland data source and answers text/uri-list itself
// with the file being dragged, which GG's drag handler (patched by
// gg-linux) writes to $XDG_RUNTIME_DIR/gg-linux-drag-<pid> for the length
// of the drag. Other requests go to Chromium as before.
//
// Build: cc -shared -fPIC -O2 -o libgg-dragfix.so dragfix.c -ldl

#define _GNU_SOURCE
#include <dlfcn.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

struct wl_proxy;

// libwayland-client's, called through to.
extern const char *wl_proxy_get_class(struct wl_proxy *proxy);

typedef void (*send_fn)(void *data, struct wl_proxy *source, const char *mime, int32_t fd);

// wl_data_source's events (version 3): target, send, cancelled,
// dnd_drop_performed, dnd_finished, action.
#define DATA_SOURCE_EVENTS 6
#define SEND_EVENT 1

struct wrapped {
	struct wl_proxy *proxy;
	void (*listener[DATA_SOURCE_EVENTS])(void);
	send_fn send;
};

#define MAX_SOURCES 64
static struct wrapped sources[MAX_SOURCES];
static int next_slot;

static struct wrapped *find(struct wl_proxy *proxy) {
	for (int i = 0; i < MAX_SOURCES; i++)
		if (sources[i].proxy == proxy)
			return &sources[i];
	return NULL;
}

// dragged reads the file being dragged, if a drag of GG's is on.
static int dragged(char *path, size_t size) {
	const char *dir = getenv("XDG_RUNTIME_DIR");
	char marker[512];
	snprintf(marker, sizeof marker, "%s/gg-linux-drag-%d", dir ? dir : "/tmp", (int)getpid());
	FILE *f = fopen(marker, "r");
	if (!f)
		return 0;
	size_t n = fread(path, 1, size - 1, f);
	fclose(f);
	path[n] = 0;
	while (n > 0 && (path[n - 1] == '\n' || path[n - 1] == '\r'))
		path[--n] = 0;
	return n > 0 && path[0] == '/';
}

static void write_all(int fd, const char *s, size_t n) {
	while (n > 0) {
		ssize_t w = write(fd, s, n);
		if (w <= 0)
			return;
		s += w;
		n -= (size_t)w;
	}
}

static void send_uri(int fd, const char *path) {
	static const char hex[] = "0123456789ABCDEF";
	char out[16384];
	size_t o = 0;
	const char *prefix = "file://";
	memcpy(out, prefix, strlen(prefix));
	o = strlen(prefix);
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

static void hooked_send(void *data, struct wl_proxy *source, const char *mime, int32_t fd) {
	char path[4096];
	if (mime && strcmp(mime, "text/uri-list") == 0 && dragged(path, sizeof path)) {
		send_uri(fd, path);
		close(fd);
		return;
	}
	struct wrapped *w = find(source);
	if (w && w->send)
		w->send(data, source, mime, fd);
	else
		close(fd);
}

int wl_proxy_add_listener(struct wl_proxy *proxy, void (**implementation)(void), void *data) {
	static int (*real)(struct wl_proxy *, void (**)(void), void *);
	if (!real)
		real = (int (*)(struct wl_proxy *, void (**)(void), void *))dlsym(RTLD_NEXT, "wl_proxy_add_listener");
	const char *cls = wl_proxy_get_class(proxy);
	if (!implementation || !cls || strcmp(cls, "wl_data_source") != 0)
		return real(proxy, implementation, data);
	struct wrapped *w = find(proxy);
	if (!w) {
		w = &sources[next_slot];
		next_slot = (next_slot + 1) % MAX_SOURCES;
	}
	w->proxy = proxy;
	memcpy(w->listener, implementation, sizeof w->listener);
	w->send = (send_fn)implementation[SEND_EVENT];
	w->listener[SEND_EVENT] = (void (*)(void))hooked_send;
	return real(proxy, w->listener, data);
}
