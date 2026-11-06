/*
 * NOCTURNE for macOS — the application window.
 *
 * A small Cocoa app written in C against the Objective-C runtime, so it can
 * be built without Xcode. It starts the library server (nocturne-server,
 * next to this executable), shows the editor in a WebKit window and makes
 * sure the last changes are saved before the app quits.
 */

typedef void *id;
typedef void *SEL;
typedef void *Class;
typedef void *IMP;
#if defined(__arm64__) || defined(__aarch64__)
typedef _Bool BOOL;
#define B_ "B"
#else
typedef signed char BOOL;
#define B_ "c"
#endif
#define YES ((BOOL)1)
#define NO ((BOOL)0)
#define nil ((id)0)
#define NULL ((void *)0)
typedef long NSInteger;
typedef unsigned long NSUInteger;
typedef double CGFloat;
typedef struct { CGFloat x, y; } CGPoint;
typedef struct { CGFloat width, height; } CGSize;
typedef struct { CGPoint origin; CGSize size; } CGRect;
typedef unsigned long size_t;

extern id objc_getClass(const char *name);
extern SEL sel_registerName(const char *name);
extern void objc_msgSend(void);
#if defined(__x86_64__)
extern void objc_msgSend_stret(void);
#endif
extern Class objc_allocateClassPair(Class superclass, const char *name, size_t extra);
extern void objc_registerClassPair(Class cls);
extern BOOL class_addMethod(Class cls, SEL name, IMP imp, const char *types);
extern void *objc_autoreleasePoolPush(void);
extern void objc_autoreleasePoolPop(void *pool);

extern int strcmp(const char *, const char *);
extern int snprintf(char *, size_t, const char *, ...);
extern int getpid(void);
extern int usleep(unsigned int);
extern id NSHomeDirectory(void);
extern id NSTemporaryDirectory(void);
extern void NSLog(id format, ...);

/* objc_msgSend with an explicit prototype: MSG(return type, arg types...) */
#define MSG(R, ...) ((R(*)(id, SEL, ##__VA_ARGS__))objc_msgSend)

static id cls(const char *n) { return objc_getClass(n); }
static SEL sel(const char *n) { return sel_registerName(n); }
static id S(const char *utf8) { return MSG(id, const char *)(cls("NSString"), sel("stringWithUTF8String:"), utf8); }
static const char *C(id s) {
    if (!s) return "";
    const char *r = MSG(const char *)(s, sel("UTF8String"));
    return r ? r : "";
}
static id get(id o, const char *s) { return o ? MSG(id)(o, sel(s)) : nil; }
static id new_(const char *c) { return MSG(id)(MSG(id)(cls(c), sel("alloc")), sel("init")); }
static BOOL responds(id o, const char *s) { return o && MSG(BOOL, SEL)(o, sel("respondsToSelector:"), sel(s)); }
static BOOL is_kind(id o, const char *c) { return o && MSG(BOOL, id)(o, sel("isKindOfClass:"), cls(c)); }
static void call0(id o, const char *s) { if (o) MSG(void)(o, sel(s)); }
static void call1(id o, const char *s, id a) { if (o) MSG(void, id)(o, sel(s), a); }
static void callB(id o, const char *s, BOOL b) { if (o) MSG(void, BOOL)(o, sel(s), b); }
static id key_of(id dict, const char *k) { return is_kind(dict, "NSDictionary") ? MSG(id, id)(dict, sel("objectForKey:"), S(k)) : nil; }
static BOOL streq(const char *a, const char *b) { return a && b && strcmp(a, b) == 0; }

static CGRect rect_of(id o, const char *s) {
#if defined(__x86_64__)
    return ((CGRect(*)(id, SEL))objc_msgSend_stret)(o, sel(s));
#else
    return MSG(CGRect)(o, sel(s));
#endif
}

/* Blocks we receive from WebKit (we only call them, never create them). */
typedef void (^VoidBlock)(void);
typedef void (^BoolBlock)(BOOL);
typedef void (^IdBlock)(id);
typedef void (^PolicyBlock)(NSInteger);

enum {
    StyleTitled = 1, StyleClosable = 2, StyleMiniaturizable = 4, StyleResizable = 8,
    BackingBuffered = 2,
    ModShift = 1 << 17, ModControl = 1 << 18, ModOption = 1 << 19, ModCommand = 1 << 20,
    AlertFirst = 1000, AlertSecond = 1001,
    ModalOK = 1, TerminateCancel = 0, TerminateNow = 1, TerminateLater = 2,
    PolicyCancel = 0, PolicyAllow = 1,
    AlertWarning = 0, AlertInfo = 1, AlertCritical = 2,
};

#define COMMON_MODES "kCFRunLoopCommonModes"

/* ------------------------------------------------------------------ state */

static id g_app, g_delegate, g_window, g_webView, g_task, g_pollTimer, g_watchTimer, g_quitTimer;
static char g_statusPath[4096];
static double g_startedAt;
static int g_port;
static BOOL g_pageHello, g_quitting, g_readyToQuit, g_serverLost, g_takeoverTried, g_failed;

static double now_s(void) { return MSG(double)(cls("NSDate"), sel("timeIntervalSinceReferenceDate")); }
static id defaults(void) { return MSG(id)(cls("NSUserDefaults"), sel("standardUserDefaults")); }
static id file_manager(void) { return MSG(id)(cls("NSFileManager"), sel("defaultManager")); }
static id workspace(void) { return MSG(id)(cls("NSWorkspace"), sel("sharedWorkspace")); }
static id path_join(id base, const char *rel) { return MSG(id, id)(base, sel("stringByAppendingPathComponent:"), S(rel)); }

/* A repeating or one-shot timer that also fires while a modal panel or a
 * "terminate later" loop is running. */
static id timer(double seconds, const char *selector, BOOL repeats) {
    id t = MSG(id, double, id, SEL, id, BOOL)(cls("NSTimer"), sel("timerWithTimeInterval:target:selector:userInfo:repeats:"),
                                             seconds, g_delegate, sel(selector), nil, repeats);
    id rl = MSG(id)(cls("NSRunLoop"), sel("currentRunLoop"));
    MSG(void, id, id)(rl, sel("addTimer:forMode:"), t, S(COMMON_MODES));
    return t;
}
static void stop_timer(id *t) {
    if (*t) { call0(*t, "invalidate"); *t = nil; }
}

/* ------------------------------------------------------------------ alerts */

static NSInteger alert_with(const char *title, const char *text, const char *b1, const char *b2, NSInteger style, id accessory) {
    id a = new_("NSAlert");
    MSG(void, NSInteger)(a, sel("setAlertStyle:"), style);
    call1(a, "setMessageText:", S(title));
    if (text && text[0]) call1(a, "setInformativeText:", S(text));
    call1(a, "addButtonWithTitle:", S(b1 ? b1 : "OK"));
    if (b2) {
        id b = MSG(id, id)(a, sel("addButtonWithTitle:"), S(b2));
        (void)b;
    }
    if (accessory) {
        call1(a, "setAccessoryView:", accessory);
        call0(a, "layout");
        call1(get(a, "window"), "setInitialFirstResponder:", accessory);
    }
    callB(g_app, "activateIgnoringOtherApps:", YES);
    NSInteger r = MSG(NSInteger)(a, sel("runModal"));
    call0(a, "release");
    return r;
}
static NSInteger alert(const char *title, const char *text, const char *b1, const char *b2, NSInteger style) {
    return alert_with(title, text, b1, b2, style, nil);
}

/* ------------------------------------------------------------------ folders */

static id home(const char *rel) { return path_join(NSHomeDirectory(), rel); }

/* Library and backup folders, read from the config.json the server uses. */
static id library_folder(BOOL backups) {
    id dataDir = nil, backupDir = nil;
    id data = MSG(id, id)(cls("NSData"), sel("dataWithContentsOfFile:"), home("Library/Application Support/NOCTURNE/config.json"));
    if (data) {
        id obj = MSG(id, id, NSUInteger, id *)(cls("NSJSONSerialization"), sel("JSONObjectWithData:options:error:"), data, 0, NULL);
        id d = key_of(obj, "dataDir"), b = key_of(obj, "backupDir");
        if (is_kind(d, "NSString") && MSG(NSUInteger)(d, sel("length")) > 0) dataDir = d;
        if (is_kind(b, "NSString") && MSG(NSUInteger)(b, sel("length")) > 0) backupDir = b;
    }
    if (!dataDir) dataDir = home("Documents/NOCTURNE");
    if (!backups) return dataDir;
    return backupDir ? backupDir : path_join(dataDir, "Резервные копии");
}

static void reveal_folder(id path) {
    MSG(BOOL, id, BOOL, id, id *)(file_manager(), sel("createDirectoryAtPath:withIntermediateDirectories:attributes:error:"), path, YES, nil, NULL);
    id url = MSG(id, id, BOOL)(cls("NSURL"), sel("fileURLWithPath:isDirectory:"), path, YES);
    MSG(BOOL, id)(workspace(), sel("openURL:"), url);
}

/* ------------------------------------------------------------------ server */

static void quit_now(void) { MSG(void, id)(g_app, sel("terminate:"), nil); }
/* Quit on the next run loop turn, outside of WebKit and window callbacks. */
static void quit_soon(void) {
    MSG(void, SEL, id, double)(g_app, sel("performSelector:withObject:afterDelay:"), sel("terminate:"), nil, 0.0);
}

static void fail_and_quit(const char *title, const char *text) {
    g_failed = YES;
    stop_timer(&g_pollTimer);
    stop_timer(&g_watchTimer);
    alert(title, text, "Закрыть", NULL, AlertCritical);
    quit_now();
}

static void start_server(BOOL takeover) {
    id bundle = MSG(id)(cls("NSBundle"), sel("mainBundle"));
    id exe = MSG(id, id)(bundle, sel("pathForAuxiliaryExecutable:"), S("nocturne-server"));
    if (!exe) {
        fail_and_quit("NOCTURNE повреждён", "В приложении не найден файл nocturne-server. Скачайте NOCTURNE заново.");
        return;
    }
    snprintf(g_statusPath, sizeof g_statusPath, "%s/nocturne-status-%d.json", C(NSTemporaryDirectory()), getpid());
    MSG(BOOL, id, id *)(file_manager(), sel("removeItemAtPath:error:"), S(g_statusPath), NULL);

    char pid[32];
    snprintf(pid, sizeof pid, "%d", getpid());
    id args = MSG(id)(cls("NSMutableArray"), sel("array"));
    call1(args, "addObject:", S("--app"));
    call1(args, "addObject:", S("--status-file"));
    call1(args, "addObject:", S(g_statusPath));
    call1(args, "addObject:", S("--parent-pid"));
    call1(args, "addObject:", S(pid));
    if (takeover) call1(args, "addObject:", S("--takeover"));

    id task = new_("NSTask");
    call1(task, "setExecutableURL:", MSG(id, id)(cls("NSURL"), sel("fileURLWithPath:"), exe));
    call1(task, "setArguments:", args);
    call1(task, "setStandardInput:", MSG(id)(cls("NSFileHandle"), sel("fileHandleWithNullDevice")));
    id err = nil;
    if (!MSG(BOOL, id *)(task, sel("launchAndReturnError:"), &err)) {
        char msg[1024];
        snprintf(msg, sizeof msg, "Не удалось запустить сервер библиотеки: %s", C(get(err, "localizedDescription")));
        call0(task, "release");
        fail_and_quit("Не удалось открыть NOCTURNE", msg);
        return;
    }
    if (g_task) call0(g_task, "release");
    g_task = task;
    g_startedAt = now_s();
    stop_timer(&g_pollTimer);
    g_pollTimer = timer(0.1, "nocturnePollStatus:", YES);
}

static void load_library_page(void) {
    char url[64];
    snprintf(url, sizeof url, "http://127.0.0.1:%d/", g_port);
    id req = MSG(id, id)(cls("NSURLRequest"), sel("requestWithURL:"), MSG(id, id)(cls("NSURL"), sel("URLWithString:"), S(url)));
    MSG(id, id)(g_webView, sel("loadRequest:"), req);
}

static void m_pollStatus(id self, SEL _cmd, id t) {
    id data = MSG(id, id)(cls("NSData"), sel("dataWithContentsOfFile:"), S(g_statusPath));
    if (!data) {
        BOOL running = MSG(BOOL)(g_task, sel("isRunning"));
        double waited = now_s() - g_startedAt;
        if ((!running && waited > 1.5) || waited > 45) {
            fail_and_quit("Не удалось открыть NOCTURNE",
                          "Сервер библиотеки не запустился. Подробности записаны в файл ~/Library/Application Support/NOCTURNE/server.log");
        }
        return;
    }
    stop_timer(&g_pollTimer);
    id obj = MSG(id, id, NSUInteger, id *)(cls("NSJSONSerialization"), sel("JSONObjectWithData:options:error:"), data, 0, NULL);
    id port = key_of(obj, "port");
    if (port && key_of(obj, "ok")) {
        g_port = MSG(int)(port, sel("intValue"));
        load_library_page();
        stop_timer(&g_watchTimer);
        g_watchTimer = timer(1.0, "nocturneWatchServer:", YES);
        return;
    }
    const char *code = C(key_of(obj, "code"));
    const char *error = C(key_of(obj, "error"));
    if (streq(code, "locked")) {
        if (!g_takeoverTried) {
            NSInteger r = alert("NOCTURNE уже открыт в браузере",
                                "Сейчас работает прежняя версия NOCTURNE. Чтобы открыть библиотеку в этом окне, её нужно закрыть — "
                                "она сначала сохранит все изменения.\n\nЕсли в браузере осталась вкладка NOCTURNE, закройте её потом: "
                                "дальше писать нужно здесь.",
                                "Закрыть прежнюю и открыть здесь", "Выйти", AlertWarning);
            if (r == AlertFirst) {
                g_takeoverTried = YES;
                start_server(YES);
                return;
            }
            g_failed = YES;
            quit_now();
            return;
        }
        fail_and_quit("Не удалось открыть библиотеку",
                      "Прежняя версия NOCTURNE не закрылась. Нажмите в ней «Завершить NOCTURNE» и откройте приложение снова.");
        return;
    }
    char msg[2048];
    snprintf(msg, sizeof msg, "%s\n\nПодробности: ~/Library/Application Support/NOCTURNE/server.log", error[0] ? error : "Неизвестная ошибка");
    fail_and_quit("Не удалось открыть NOCTURNE", msg);
}

static void m_watchServer(id self, SEL _cmd, id t) {
    if (!g_task || g_quitting || MSG(BOOL)(g_task, sel("isRunning"))) return;
    g_serverLost = YES;
    stop_timer(&g_watchTimer);
    alert("Сервер библиотеки NOCTURNE остановился",
          "Окно будет закрыто. Откройте NOCTURNE снова — библиотека на месте. Изменения последних секунд могли не сохраниться.",
          "Закрыть", NULL, AlertCritical);
    quit_now();
}

static void stop_server(void) {
    if (!g_task) return;
    if (MSG(BOOL)(g_task, sel("isRunning"))) {
        call0(g_task, "terminate");
        for (int i = 0; i < 200 && MSG(BOOL)(g_task, sel("isRunning")); i++) usleep(50000);
    }
    MSG(BOOL, id, id *)(file_manager(), sel("removeItemAtPath:error:"), S(g_statusPath), NULL);
}

/* ------------------------------------------------------------------ quitting */

static void reply_terminate(BOOL yes) {
    stop_timer(&g_quitTimer);
    if (yes) g_readyToQuit = YES; else g_quitting = NO;
    callB(g_app, "replyToApplicationShouldTerminate:", yes);
}

static NSUInteger m_shouldTerminate(id self, SEL _cmd, id sender) {
    if (g_readyToQuit || g_failed || g_serverLost || !g_pageHello || !g_task || !MSG(BOOL)(g_task, sel("isRunning")))
        return TerminateNow;
    if (g_quitting) return TerminateLater;
    g_quitting = YES;
    MSG(void, id, id)(g_webView, sel("evaluateJavaScript:completionHandler:"),
                      S("window.__nocturneBeforeQuit ? window.__nocturneBeforeQuit() : (window.webkit.messageHandlers.nocturne.postMessage({type:'ready-to-quit'}), true)"),
                      nil);
    stop_timer(&g_quitTimer);
    g_quitTimer = timer(25.0, "nocturneQuitTimeout:", NO);
    return TerminateLater;
}

static void m_quitTimeout(id self, SEL _cmd, id t) {
    g_quitTimer = nil;
    if (!g_quitting || g_readyToQuit) return;
    NSInteger r = alert("NOCTURNE ещё сохраняет изменения",
                        "Сохранение длится дольше обычного. Можно подождать ещё или выйти — тогда изменения последних секунд могут потеряться.",
                        "Подождать", "Выйти", AlertWarning);
    if (r == AlertFirst) {
        g_quitTimer = timer(20.0, "nocturneQuitTimeout:", NO);
        return;
    }
    reply_terminate(YES);
}

static void m_willTerminate(id self, SEL _cmd, id note) { stop_server(); }

static BOOL m_lastWindowClosed(id self, SEL _cmd, id app) { return YES; }

static BOOL m_reopen(id self, SEL _cmd, id app, BOOL hasVisible) {
    if (!hasVisible && g_window) call1(g_window, "makeKeyAndOrderFront:", nil);
    return YES;
}

static BOOL m_windowShouldClose(id self, SEL _cmd, id window) {
    quit_soon();
    return NO;
}

/* ------------------------------------------------------------------ page messages */

static void m_didReceiveMessage(id self, SEL _cmd, id controller, id message) {
    id body = get(message, "body");
    const char *type = C(key_of(body, "type"));
    if (streq(type, "hello")) {
        g_pageHello = YES;
        MSG(BOOL, id)(g_window, sel("makeFirstResponder:"), g_webView);
    } else if (streq(type, "quit")) {
        quit_soon();
    } else if (streq(type, "ready-to-quit")) {
        if (g_quitting) reply_terminate(YES);
    } else if (streq(type, "quit-blocked")) {
        if (!g_quitting) return;
        stop_timer(&g_quitTimer);
        char text[2048];
        snprintf(text, sizeof text,
                 "%s\n\nЧтобы ничего не потерять, нажмите «Не выходить» и сохраните копию кнопкой «⇩ JSON» или «⇩ Архив».",
                 C(key_of(body, "message")));
        NSInteger r = alert("Последние изменения не сохранены", text, "Не выходить", "Выйти без сохранения", AlertCritical);
        reply_terminate(r != AlertFirst);
    } else if (streq(type, "downloaded")) {
        id path = key_of(body, "path");
        if (is_kind(path, "NSString")) {
            id dnc = MSG(id)(cls("NSDistributedNotificationCenter"), sel("defaultCenter"));
            MSG(void, id, id)(dnc, sel("postNotificationName:object:"), S("com.apple.DownloadFileFinished"), path);
        }
    }
}

/* ------------------------------------------------------------------ WebKit UI */

static void m_jsAlert(id self, SEL _cmd, id webView, id message, id frame, void *handler) {
    alert("NOCTURNE", C(message), "OK", NULL, AlertInfo);
    ((VoidBlock)handler)();
}

static void m_jsConfirm(id self, SEL _cmd, id webView, id message, id frame, void *handler) {
    NSInteger r = alert("NOCTURNE", C(message), "OK", "Отмена", AlertInfo);
    ((BoolBlock)handler)(r == AlertFirst);
}

static void m_jsPrompt(id self, SEL _cmd, id webView, id prompt, id defaultText, id frame, void *handler) {
    CGRect r = {{0, 0}, {320, 24}};
    id field = MSG(id, CGRect)(MSG(id)(cls("NSTextField"), sel("alloc")), sel("initWithFrame:"), r);
    call1(field, "setStringValue:", defaultText ? defaultText : S(""));
    NSInteger res = alert_with("NOCTURNE", C(prompt), "OK", "Отмена", AlertInfo, field);
    id value = res == AlertFirst ? MSG(id)(MSG(id)(field, sel("stringValue")), sel("copy")) : nil;
    call0(field, "release");
    ((IdBlock)handler)(value);
    if (value) call0(value, "release");
}

static void m_beforeUnload(id self, SEL _cmd, id webView, id message, id frame, void *handler) {
    if (g_quitting) { ((BoolBlock)handler)(YES); return; }
    NSInteger r = alert("Изменения ещё сохраняются",
                        "Подождите пару секунд, пока NOCTURNE сохранит последние изменения.",
                        "Остаться", "Всё равно уйти", AlertWarning);
    ((BoolBlock)handler)(r != AlertFirst);
}

static void add_types(id list, id items, BOOL mime) {
    if (!is_kind(items, "NSArray") && !is_kind(items, "NSSet")) return;
    id all = is_kind(items, "NSSet") ? get(items, "allObjects") : items;
    NSUInteger n = MSG(NSUInteger)(all, sel("count"));
    for (NSUInteger i = 0; i < n; i++) {
        id s = MSG(id, NSUInteger)(all, sel("objectAtIndex:"), i);
        if (!is_kind(s, "NSString")) continue;
        const char *v = C(s);
        if (!mime) {
            if (v[0] == '.') v++;
            if (v[0]) call1(list, "addObject:", S(v));
            continue;
        }
        if (MSG(BOOL, id)(s, sel("hasPrefix:"), S("image/"))) call1(list, "addObject:", S("public.image"));
        else if (MSG(BOOL, id)(s, sel("hasPrefix:"), S("audio/"))) call1(list, "addObject:", S("public.audio"));
        else if (streq(v, "application/json")) call1(list, "addObject:", S("public.json"));
        else if (streq(v, "application/zip")) call1(list, "addObject:", S("public.zip-archive"));
    }
}

static void m_openPanel(id self, SEL _cmd, id webView, id params, id frame, void *handler) {
    id panel = MSG(id)(cls("NSOpenPanel"), sel("openPanel"));
    callB(panel, "setCanChooseFiles:", YES);
    BOOL dirs = responds(params, "allowsDirectories") ? MSG(BOOL)(params, sel("allowsDirectories")) : NO;
    callB(panel, "setCanChooseDirectories:", dirs);
    callB(panel, "setAllowsMultipleSelection:", responds(params, "allowsMultipleSelection") ? MSG(BOOL)(params, sel("allowsMultipleSelection")) : NO);
    callB(panel, "setResolvesAliases:", YES);
    if (!dirs) {
        id types = MSG(id)(cls("NSMutableArray"), sel("array"));
        if (responds(params, "_acceptedFileExtensions")) add_types(types, get(params, "_acceptedFileExtensions"), NO);
        if (responds(params, "_acceptedMIMETypes")) add_types(types, get(params, "_acceptedMIMETypes"), YES);
        if (MSG(NSUInteger)(types, sel("count")) > 0 && responds(panel, "setAllowedFileTypes:"))
            call1(panel, "setAllowedFileTypes:", types);
    }
    NSInteger r = MSG(NSInteger)(panel, sel("runModal"));
    ((IdBlock)handler)(r == ModalOK ? get(panel, "URLs") : nil);
}

static BOOL is_local(id url) {
    const char *scheme = C(get(url, "scheme"));
    const char *host = C(get(url, "host"));
    if (streq(scheme, "about") || streq(scheme, "blob") || streq(scheme, "data")) return YES;
    return (streq(scheme, "http") || streq(scheme, "https")) && (streq(host, "127.0.0.1") || streq(host, "localhost"));
}

static void open_external(id url) {
    if (url && !is_local(url)) MSG(BOOL, id)(workspace(), sel("openURL:"), url);
}

static id m_createWebView(id self, SEL _cmd, id webView, id configuration, id action, id features) {
    open_external(get(get(action, "request"), "URL"));
    return nil;
}

static void m_decidePolicy(id self, SEL _cmd, id webView, id action, void *handler) {
    id url = get(get(action, "request"), "URL");
    if (!url || is_local(url)) { ((PolicyBlock)handler)(PolicyAllow); return; }
    id frame = get(action, "targetFrame");
    BOOL mainFrame = !frame || MSG(BOOL)(frame, sel("isMainFrame"));
    if (mainFrame) {
        open_external(url);
        ((PolicyBlock)handler)(PolicyCancel);
        return;
    }
    ((PolicyBlock)handler)(PolicyAllow);
}

static void m_contentProcessDidTerminate(id self, SEL _cmd, id webView) {
    if (g_port) load_library_page();
}

static void m_didFailProvisional(id self, SEL _cmd, id webView, id nav, id error) {
    NSLog(S("NOCTURNE: page failed to load: %@"), error);
}

static void do_print(void) {
    if (!responds(g_webView, "printOperationWithPrintInfo:")) return;
    id info = MSG(id)(MSG(id)(cls("NSPrintInfo"), sel("sharedPrintInfo")), sel("copy"));
    MSG(void, NSInteger)(info, sel("setHorizontalPagination:"), 1);
    MSG(void, NSInteger)(info, sel("setVerticalPagination:"), 0);
    callB(info, "setVerticallyCentered:", NO);
    id op = MSG(id, id)(g_webView, sel("printOperationWithPrintInfo:"), info);
    callB(op, "setShowsPrintPanel:", YES);
    callB(op, "setShowsProgressPanel:", YES);
    id view = get(op, "view");
    if (view) MSG(void, CGRect)(view, sel("setFrame:"), rect_of(g_webView, "bounds"));
    MSG(void, id, id, SEL, void *)(op, sel("runOperationModalForWindow:delegate:didRunSelector:contextInfo:"), g_window, nil, NULL, NULL);
    call0(info, "release");
}

static void m_printFrame(id self, SEL _cmd, id webView, id frame) { do_print(); }

/* ------------------------------------------------------------------ menu actions */

static double page_zoom(void) {
    return responds(g_webView, "pageZoom") ? MSG(double)(g_webView, sel("pageZoom")) : 1.0;
}
static void set_page_zoom(double z) {
    if (z < 0.5) z = 0.5;
    if (z > 2.5) z = 2.5;
    if (!responds(g_webView, "setPageZoom:")) return;
    MSG(void, double)(g_webView, sel("setPageZoom:"), z);
    MSG(void, double, id)(defaults(), sel("setDouble:forKey:"), z, S("NOCTURNEPageZoom"));
}
static void m_zoomIn(id self, SEL _cmd, id sender) { set_page_zoom(page_zoom() * 1.1); }
static void m_zoomOut(id self, SEL _cmd, id sender) { set_page_zoom(page_zoom() / 1.1); }
static void m_zoomReset(id self, SEL _cmd, id sender) { set_page_zoom(1.0); }
static void m_reload(id self, SEL _cmd, id sender) {
    if (!g_port) return;
    MSG(void, id, id)(g_webView, sel("evaluateJavaScript:completionHandler:"),
                      S("(window.__nocturneSettle ? window.__nocturneSettle(15000) : Promise.resolve()).then(function(){ location.reload(); }), true"), nil);
}
static void m_print(id self, SEL _cmd, id sender) { do_print(); }
static void m_showLibrary(id self, SEL _cmd, id sender) { reveal_folder(library_folder(NO)); }
static void m_showBackups(id self, SEL _cmd, id sender) { reveal_folder(library_folder(YES)); }

/* ------------------------------------------------------------------ menu */

static id menu_item(id menu, const char *title, const char *action, const char *key, NSUInteger mods, id target) {
    id item = MSG(id, id, SEL, id)(MSG(id)(cls("NSMenuItem"), sel("alloc")), sel("initWithTitle:action:keyEquivalent:"),
                                   S(title), action ? sel(action) : NULL, S(key ? key : ""));
    if (key && key[0]) MSG(void, NSUInteger)(item, sel("setKeyEquivalentModifierMask:"), mods ? mods : ModCommand);
    if (target) call1(item, "setTarget:", target);
    call1(menu, "addItem:", item);
    call0(item, "release");
    return item;
}
static void separator(id menu) { call1(menu, "addItem:", MSG(id)(cls("NSMenuItem"), sel("separatorItem"))); }
static id submenu(id parent, const char *title) {
    id item = menu_item(parent, title, NULL, NULL, 0, nil);
    id m = MSG(id, id)(MSG(id)(cls("NSMenu"), sel("alloc")), sel("initWithTitle:"), S(title));
    call1(item, "setSubmenu:", m);
    call0(m, "release");
    return m;
}

static void build_menu(void) {
    id bar = MSG(id, id)(MSG(id)(cls("NSMenu"), sel("alloc")), sel("initWithTitle:"), S("NOCTURNE"));

    id app = submenu(bar, "NOCTURNE");
    menu_item(app, "О программе NOCTURNE", "orderFrontStandardAboutPanel:", NULL, 0, g_app);
    separator(app);
    id services = submenu(app, "Службы");
    call1(g_app, "setServicesMenu:", services);
    separator(app);
    menu_item(app, "Скрыть NOCTURNE", "hide:", "h", 0, g_app);
    menu_item(app, "Скрыть остальные", "hideOtherApplications:", "h", ModCommand | ModOption, g_app);
    menu_item(app, "Показать все", "unhideAllApplications:", NULL, 0, g_app);
    separator(app);
    menu_item(app, "Завершить NOCTURNE", "terminate:", "q", 0, g_app);

    id file = submenu(bar, "Файл");
    menu_item(file, "Открыть папку библиотеки", "nocturneShowLibrary:", NULL, 0, g_delegate);
    menu_item(file, "Открыть папку резервных копий", "nocturneShowBackups:", NULL, 0, g_delegate);
    separator(file);
    menu_item(file, "Печать…", "nocturnePrint:", "p", 0, g_delegate);
    separator(file);
    menu_item(file, "Закрыть окно", "performClose:", "w", 0, nil);

    id edit = submenu(bar, "Правка");
    menu_item(edit, "Отменить", "undo:", "z", 0, nil);
    menu_item(edit, "Повторить", "redo:", "z", ModCommand | ModShift, nil);
    separator(edit);
    menu_item(edit, "Вырезать", "cut:", "x", 0, nil);
    menu_item(edit, "Скопировать", "copy:", "c", 0, nil);
    menu_item(edit, "Вставить", "paste:", "v", 0, nil);
    menu_item(edit, "Вставить без оформления", "pasteAsPlainText:", "v", ModCommand | ModOption | ModShift, nil);
    menu_item(edit, "Выбрать все", "selectAll:", "a", 0, nil);
    separator(edit);
    id spell = submenu(edit, "Правописание");
    menu_item(spell, "Проверить сейчас", "checkSpelling:", ";", 0, nil);
    menu_item(spell, "Проверять при вводе", "toggleContinuousSpellChecking:", NULL, 0, nil);
    menu_item(spell, "Автоматически исправлять", "toggleAutomaticSpellingCorrection:", NULL, 0, nil);

    id view = submenu(bar, "Вид");
    menu_item(view, "Увеличить", "nocturneZoomIn:", "+", 0, g_delegate);
    id alt = menu_item(view, "Увеличить", "nocturneZoomIn:", "=", 0, g_delegate);
    callB(alt, "setHidden:", YES);
    if (responds(alt, "setAllowsKeyEquivalentWhenHidden:")) callB(alt, "setAllowsKeyEquivalentWhenHidden:", YES);
    menu_item(view, "Уменьшить", "nocturneZoomOut:", "-", 0, g_delegate);
    menu_item(view, "Исходный размер", "nocturneZoomReset:", "0", 0, g_delegate);
    separator(view);
    menu_item(view, "Обновить страницу", "nocturneReload:", "r", 0, g_delegate);
    separator(view);
    menu_item(view, "Во весь экран", "toggleFullScreen:", "f", ModCommand | ModControl, nil);

    id win = submenu(bar, "Окно");
    menu_item(win, "Свернуть", "performMiniaturize:", "m", 0, nil);
    menu_item(win, "Масштабировать", "performZoom:", NULL, 0, nil);
    separator(win);
    menu_item(win, "Все окна — на передний план", "arrangeInFront:", NULL, 0, g_app);
    call1(g_app, "setWindowsMenu:", win);

    call1(g_app, "setMainMenu:", bar);
    call0(bar, "release");
}

/* ------------------------------------------------------------------ window */

static const char *LOADING_HTML =
    "<!doctype html><html><head><meta charset='utf-8'></head>"
    "<body style=\"margin:0;height:100vh;display:grid;place-items:center;background:#16120f;color:#d9ab71;"
    "font:22px Georgia,serif;-webkit-user-select:none;cursor:default\">"
    "<div style='text-align:center'><div style='font-size:44px'>☾</div>"
    "<div style='letter-spacing:7px;margin-top:14px'>NOCTURNE</div>"
    "<div style='font:13px -apple-system,sans-serif;color:#8c7767;margin-top:16px'>Открываю библиотеку…</div></div>"
    "</body></html>";

static id bg_color(void) {
    return MSG(id, CGFloat, CGFloat, CGFloat, CGFloat)(cls("NSColor"), sel("colorWithSRGBRed:green:blue:alpha:"),
                                                      0x16 / 255.0, 0x12 / 255.0, 0x0f / 255.0, 1.0);
}

static void create_window(void) {
    MSG(void, BOOL)(cls("NSWindow"), sel("setAllowsAutomaticWindowTabbing:"), NO);
    id screen = MSG(id)(cls("NSScreen"), sel("mainScreen"));
    CGRect vis = screen ? rect_of(screen, "visibleFrame") : (CGRect){{0, 0}, {1440, 900}};
    double w = vis.size.width - 80, h = vis.size.height - 50;
    if (w > 1480) w = 1480;
    if (h > 980) h = 980;
    if (w < 900) w = vis.size.width;
    if (h < 600) h = vis.size.height;
    CGRect frame = {{vis.origin.x + (vis.size.width - w) / 2, vis.origin.y + (vis.size.height - h) / 2}, {w, h}};

    g_window = MSG(id, CGRect, NSUInteger, NSUInteger, BOOL)(MSG(id)(cls("NSWindow"), sel("alloc")),
                                                             sel("initWithContentRect:styleMask:backing:defer:"), frame,
                                                             StyleTitled | StyleClosable | StyleMiniaturizable | StyleResizable,
                                                             BackingBuffered, NO);
    call1(g_window, "setTitle:", S("NOCTURNE"));
    callB(g_window, "setReleasedWhenClosed:", NO);
    MSG(void, CGSize)(g_window, sel("setContentMinSize:"), (CGSize){900, 600});
    call1(g_window, "setBackgroundColor:", bg_color());
    id dark = MSG(id, id)(cls("NSAppearance"), sel("appearanceNamed:"), S("NSAppearanceNameDarkAqua"));
    if (dark) call1(g_window, "setAppearance:", dark);
    call1(g_window, "setDelegate:", g_delegate);
    MSG(BOOL, id)(g_window, sel("setFrameAutosaveName:"), S("NOCTURNE main window"));

    id conf = new_("WKWebViewConfiguration");
    id ucc = get(conf, "userContentController");
    MSG(void, id, id)(ucc, sel("addScriptMessageHandler:name:"), g_delegate, S("nocturne"));
    id prefs = get(conf, "preferences");
    if (responds(prefs, "setElementFullscreenEnabled:")) callB(prefs, "setElementFullscreenEnabled:", YES);
    if (responds(prefs, "setTextInteractionEnabled:")) callB(prefs, "setTextInteractionEnabled:", YES);
    BOOL developer = MSG(BOOL, id)(defaults(), sel("boolForKey:"), S("NOCTURNEDeveloper"));
    if (developer) MSG(void, id, id)(prefs, sel("setValue:forKey:"), MSG(id, BOOL)(cls("NSNumber"), sel("numberWithBool:"), YES), S("developerExtrasEnabled"));
    if (responds(conf, "setMediaTypesRequiringUserActionForPlayback:"))
        MSG(void, NSUInteger)(conf, sel("setMediaTypesRequiringUserActionForPlayback:"), 0);
    if (responds(conf, "setApplicationNameForUserAgent:")) call1(conf, "setApplicationNameForUserAgent:", S("NOCTURNE"));

    CGRect content = {{0, 0}, {w, h}};
    g_webView = MSG(id, CGRect, id)(MSG(id)(cls("WKWebView"), sel("alloc")), sel("initWithFrame:configuration:"), content, conf);
    call0(conf, "release");
    MSG(void, NSUInteger)(g_webView, sel("setAutoresizingMask:"), 2 | 16);
    call1(g_webView, "setUIDelegate:", g_delegate);
    call1(g_webView, "setNavigationDelegate:", g_delegate);
    callB(g_webView, "setAllowsBackForwardNavigationGestures:", NO);
    if (responds(g_webView, "setAllowsMagnification:")) callB(g_webView, "setAllowsMagnification:", NO);
    if (responds(g_webView, "_setDrawsBackground:")) callB(g_webView, "_setDrawsBackground:", NO);
    if (responds(g_webView, "setUnderPageBackgroundColor:")) call1(g_webView, "setUnderPageBackgroundColor:", bg_color());
    if (developer && responds(g_webView, "setInspectable:")) callB(g_webView, "setInspectable:", YES);
    double zoom = MSG(double, id)(defaults(), sel("doubleForKey:"), S("NOCTURNEPageZoom"));
    if (zoom >= 0.5 && zoom <= 2.5 && responds(g_webView, "setPageZoom:")) MSG(void, double)(g_webView, sel("setPageZoom:"), zoom);

    call1(g_window, "setContentView:", g_webView);
    MSG(id, id, id)(g_webView, sel("loadHTMLString:baseURL:"), S(LOADING_HTML), nil);
    call1(g_window, "makeKeyAndOrderFront:", nil);
    callB(g_app, "activateIgnoringOtherApps:", YES);
}

static void m_didFinishLaunching(id self, SEL _cmd, id note) {
    create_window();
    start_server(NO);
}

/* ------------------------------------------------------------------ main */

static void add(Class c, const char *name, void *fn, const char *types) { class_addMethod(c, sel(name), (IMP)fn, types); }

int main(int argc, char **argv) {
    void *pool = objc_autoreleasePoolPush();
    g_app = MSG(id)(cls("NSApplication"), sel("sharedApplication"));
    MSG(BOOL, NSInteger)(g_app, sel("setActivationPolicy:"), 0);

    Class c = objc_allocateClassPair(cls("NSObject"), "NocturneAppDelegate", 0);
    add(c, "applicationDidFinishLaunching:", m_didFinishLaunching, "v@:@");
    add(c, "applicationShouldTerminate:", m_shouldTerminate, "Q@:@");
    add(c, "applicationWillTerminate:", m_willTerminate, "v@:@");
    add(c, "applicationShouldTerminateAfterLastWindowClosed:", m_lastWindowClosed, B_ "@:@");
    add(c, "applicationShouldHandleReopen:hasVisibleWindows:", m_reopen, B_ "@:@" B_);
    add(c, "windowShouldClose:", m_windowShouldClose, B_ "@:@");
    add(c, "nocturnePollStatus:", m_pollStatus, "v@:@");
    add(c, "nocturneWatchServer:", m_watchServer, "v@:@");
    add(c, "nocturneQuitTimeout:", m_quitTimeout, "v@:@");
    add(c, "userContentController:didReceiveScriptMessage:", m_didReceiveMessage, "v@:@@");
    add(c, "webView:runJavaScriptAlertPanelWithMessage:initiatedByFrame:completionHandler:", m_jsAlert, "v@:@@@@?");
    add(c, "webView:runJavaScriptConfirmPanelWithMessage:initiatedByFrame:completionHandler:", m_jsConfirm, "v@:@@@@?");
    add(c, "webView:runJavaScriptTextInputPanelWithPrompt:defaultText:initiatedByFrame:completionHandler:", m_jsPrompt, "v@:@@@@@?");
    add(c, "_webView:runBeforeUnloadConfirmPanelWithMessage:initiatedByFrame:completionHandler:", m_beforeUnload, "v@:@@@@?");
    add(c, "webView:runOpenPanelWithParameters:initiatedByFrame:completionHandler:", m_openPanel, "v@:@@@@?");
    add(c, "webView:createWebViewWithConfiguration:forNavigationAction:windowFeatures:", m_createWebView, "@@:@@@@");
    add(c, "webView:decidePolicyForNavigationAction:decisionHandler:", m_decidePolicy, "v@:@@@?");
    add(c, "webViewWebContentProcessDidTerminate:", m_contentProcessDidTerminate, "v@:@");
    add(c, "webView:didFailProvisionalNavigation:withError:", m_didFailProvisional, "v@:@@@");
    add(c, "_webView:printFrame:", m_printFrame, "v@:@@");
    add(c, "nocturneZoomIn:", m_zoomIn, "v@:@");
    add(c, "nocturneZoomOut:", m_zoomOut, "v@:@");
    add(c, "nocturneZoomReset:", m_zoomReset, "v@:@");
    add(c, "nocturneReload:", m_reload, "v@:@");
    add(c, "nocturnePrint:", m_print, "v@:@");
    add(c, "nocturneShowLibrary:", m_showLibrary, "v@:@");
    add(c, "nocturneShowBackups:", m_showBackups, "v@:@");
    objc_registerClassPair(c);

    g_delegate = MSG(id)(MSG(id)((id)c, sel("alloc")), sel("init"));
    call1(g_app, "setDelegate:", g_delegate);
    build_menu();
    call0(g_app, "run");
    objc_autoreleasePoolPop(pool);
    return 0;
}
