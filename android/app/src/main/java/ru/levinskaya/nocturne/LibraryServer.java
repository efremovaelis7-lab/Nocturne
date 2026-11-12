package ru.levinskaya.nocturne;

import android.content.Context;
import android.content.pm.PackageInfo;
import android.content.res.AssetManager;
import android.os.SystemClock;
import android.util.Log;

import org.json.JSONObject;

import java.io.BufferedReader;
import java.io.File;
import java.io.FileInputStream;
import java.io.FileOutputStream;
import java.io.IOException;
import java.io.InputStream;
import java.io.InputStreamReader;
import java.io.OutputStream;
import java.nio.charset.StandardCharsets;
import java.security.SecureRandom;
import java.util.Map;
import java.util.TimeZone;
import java.util.concurrent.TimeUnit;

/**
 * Starts the same library server the Mac and Windows builds use (desktop/,
 * compiled for Android as libnocturne.so) and keeps it running while the app
 * process lives.
 *
 * Folders inside the app's private storage:
 *   files/home/Documents/NOCTURNE   the library (library.json, Музыка, Изображения, Резервные копии)
 *   files/home/.config/NOCTURNE     settings and server.log
 *   files/home/Downloads            files the page "downloads"; the app then asks where to save them
 *   files/www                       index.html and app.js, copied from the APK
 */
final class LibraryServer {
    private static final String TAG = "NOCTURNE";

    static final class Info {
        final String url;
        final int port;
        final String token;

        Info(String url, int port, String token) {
            this.url = url;
            this.port = port;
            this.token = token;
        }
    }

    private static Process process;
    private static Info info;

    private LibraryServer() {}

    static File home(Context ctx) {
        return new File(ctx.getFilesDir(), "home");
    }

    static File downloads(Context ctx) {
        return new File(home(ctx), "Downloads");
    }

    static synchronized Info current() {
        return process != null && process.isAlive() ? info : null;
    }

    static synchronized Info start(Context context) throws Exception {
        if (process != null && process.isAlive() && info != null) {
            return info;
        }
        Context ctx = context.getApplicationContext();
        File home = home(ctx);
        mkdirs(home);
        mkdirs(downloads(ctx));
        File www = new File(ctx.getFilesDir(), "www");
        copyInterface(ctx, www);

        File exe = new File(ctx.getApplicationInfo().nativeLibraryDir, "libnocturne.so");
        if (!exe.exists()) {
            throw new IOException("В приложении нет сервера библиотеки для этого телефона (" + exe + ")");
        }
        File status = new File(ctx.getCacheDir(), "server-status.json");
        //noinspection ResultOfMethodCallIgnored
        status.delete();
        String token = randomHex(32);

        ProcessBuilder pb = new ProcessBuilder(
                exe.getAbsolutePath(),
                "-app",
                "-platform", "android",
                "-resources", www.getAbsolutePath(),
                "-status-file", status.getAbsolutePath(),
                "-parent-pid", String.valueOf(android.os.Process.myPid()));
        Map<String, String> env = pb.environment();
        env.put("HOME", home.getAbsolutePath());
        env.put("TMPDIR", ctx.getCacheDir().getAbsolutePath());
        env.put("NOCTURNE_ACCESS_TOKEN", token);
        env.put("NOCTURNE_TZ", TimeZone.getDefault().getID());
        pb.directory(home);
        pb.redirectErrorStream(true);
        Process p = pb.start();
        drain(p);

        long deadline = SystemClock.uptimeMillis() + 20000;
        while (SystemClock.uptimeMillis() < deadline) {
            if (status.length() > 0) {
                JSONObject j = new JSONObject(readText(status));
                if (j.optBoolean("ok")) {
                    int port = j.getInt("port");
                    process = p;
                    info = new Info("http://127.0.0.1:" + port + "/", port, token);
                    Log.i(TAG, "library server on " + info.url);
                    return info;
                }
                p.destroy();
                throw new IOException(j.optString("error", "Сервер библиотеки не запустился"));
            }
            if (!p.isAlive() && status.length() == 0) {
                throw new IOException("Сервер библиотеки остановился сразу после запуска (код " + p.exitValue() + "). Подробности — в server.log.");
            }
            Thread.sleep(40);
        }
        p.destroy();
        throw new IOException("Сервер библиотеки не ответил за 20 секунд");
    }

    /** Stops the server; it finishes the request in progress first (SIGTERM). */
    static synchronized void stop() {
        Process p = process;
        process = null;
        info = null;
        if (p == null) {
            return;
        }
        p.destroy();
        try {
            if (!p.waitFor(4, TimeUnit.SECONDS)) {
                p.destroyForcibly();
            }
        } catch (InterruptedException ignored) {
            Thread.currentThread().interrupt();
        }
    }

    /** Copies index.html and app.js out of the APK once per installed version. */
    private static void copyInterface(Context ctx, File www) throws Exception {
        PackageInfo pi = ctx.getPackageManager().getPackageInfo(ctx.getPackageName(), 0);
        String stamp = pi.lastUpdateTime + "/" + pi.versionCode;
        File marker = new File(www, ".installed");
        if (marker.exists() && stamp.equals(readText(marker).trim())) {
            return;
        }
        mkdirs(www);
        AssetManager am = ctx.getAssets();
        String[] names = am.list("www");
        if (names == null || names.length == 0) {
            throw new IOException("В приложении нет файлов интерфейса");
        }
        for (String name : names) {
            File dest = new File(www, name);
            File tmp = new File(www, name + ".tmp");
            try (InputStream in = am.open("www/" + name); OutputStream out = new FileOutputStream(tmp)) {
                copy(in, out);
            }
            if (!tmp.renameTo(dest)) {
                throw new IOException("Не удалось обновить " + dest);
            }
        }
        try (OutputStream out = new FileOutputStream(marker)) {
            out.write(stamp.getBytes(StandardCharsets.UTF_8));
        }
    }

    private static void drain(Process p) {
        Thread t = new Thread(() -> {
            try (BufferedReader r = new BufferedReader(new InputStreamReader(p.getInputStream(), StandardCharsets.UTF_8))) {
                String line;
                while ((line = r.readLine()) != null) {
                    Log.i(TAG, "server: " + line);
                }
            } catch (IOException ignored) {
            }
        }, "nocturne-server-output");
        t.setDaemon(true);
        t.start();
    }

    static void copy(InputStream in, OutputStream out) throws IOException {
        byte[] buf = new byte[256 * 1024];
        int n;
        while ((n = in.read(buf)) > 0) {
            out.write(buf, 0, n);
        }
    }

    private static String readText(File f) throws IOException {
        try (InputStream in = new FileInputStream(f)) {
            java.io.ByteArrayOutputStream b = new java.io.ByteArrayOutputStream();
            copy(in, b);
            return b.toString("UTF-8");
        }
    }

    private static void mkdirs(File dir) throws IOException {
        if (!dir.isDirectory() && !dir.mkdirs()) {
            throw new IOException("Не удалось создать папку " + dir);
        }
    }

    private static String randomHex(int bytes) {
        byte[] b = new byte[bytes];
        new SecureRandom().nextBytes(b);
        StringBuilder sb = new StringBuilder();
        for (byte x : b) {
            sb.append(String.format("%02x", x));
        }
        return sb.toString();
    }
}
