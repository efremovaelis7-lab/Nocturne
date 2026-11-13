package ru.levinskaya.nocturne;

import android.app.Activity;
import android.content.ActivityNotFoundException;
import android.content.ClipData;
import android.content.Intent;
import android.graphics.Insets;
import android.graphics.Typeface;
import android.net.Uri;
import android.os.Build;
import android.os.Bundle;
import android.util.Log;
import android.view.Gravity;
import android.view.View;
import android.view.ViewGroup;
import android.view.Window;
import android.view.WindowInsets;
import android.webkit.ConsoleMessage;
import android.webkit.CookieManager;
import android.webkit.JavascriptInterface;
import android.webkit.MimeTypeMap;
import android.webkit.RenderProcessGoneDetail;
import android.webkit.ValueCallback;
import android.webkit.WebChromeClient;
import android.webkit.WebResourceRequest;
import android.webkit.WebSettings;
import android.webkit.WebView;
import android.webkit.WebViewClient;
import android.widget.FrameLayout;
import android.widget.TextView;
import android.widget.Toast;

import org.json.JSONObject;

import java.io.File;
import java.io.FileInputStream;
import java.io.InputStream;
import java.io.OutputStream;
import java.util.ArrayDeque;
import java.util.ArrayList;
import java.util.List;
import java.util.Locale;

/**
 * The NOCTURNE window on Android: a full-screen WebView showing the same
 * interface as on Mac and Windows, served by the app's own library server.
 */
public class MainActivity extends Activity {
    private static final String TAG = "NOCTURNE";
    private static final int REQ_OPEN = 1;
    private static final int REQ_SAVE = 2;
    private static final int BG = 0xFF16120F;
    private static final int INK = 0xFFF0DCC0;

    private FrameLayout root;
    private WebView web;
    private TextView splash;
    private LibraryServer.Info server;

    private ValueCallback<Uri[]> fileCallback;
    private final ArrayDeque<File> saveQueue = new ArrayDeque<>();
    private File saving;

    @Override
    protected void onCreate(Bundle state) {
        super.onCreate(state);
        Window w = getWindow();
        root = new FrameLayout(this);
        root.setBackgroundColor(BG);

        web = new WebView(this);
        web.setBackgroundColor(BG);
        web.setVisibility(View.INVISIBLE);
        root.addView(web, new FrameLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT));

        splash = new TextView(this);
        splash.setText("☾\n\nNOCTURNE");
        splash.setTextColor(INK);
        splash.setTextSize(26);
        splash.setTypeface(Typeface.SERIF);
        splash.setGravity(Gravity.CENTER);
        splash.setPadding(48, 48, 48, 48);
        root.addView(splash, new FrameLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT));
        setContentView(root);

        // Android 15 draws apps under the status and navigation bars; keep the
        // page between them and above the on-screen keyboard.
        if (Build.VERSION.SDK_INT >= 30) {
            w.setDecorFitsSystemWindows(false);
            root.setOnApplyWindowInsetsListener((v, insets) -> {
                Insets bars = insets.getInsets(WindowInsets.Type.systemBars() | WindowInsets.Type.displayCutout());
                Insets ime = insets.getInsets(WindowInsets.Type.ime());
                v.setPadding(bars.left, bars.top, bars.right, Math.max(bars.bottom, ime.bottom));
                return WindowInsets.CONSUMED;
            });
        }

        setupWebView();
        startServer();
    }

    private void setupWebView() {
        WebSettings s = web.getSettings();
        s.setJavaScriptEnabled(true);
        s.setDomStorageEnabled(true);
        s.setDatabaseEnabled(true);
        s.setMediaPlaybackRequiresUserGesture(false);
        s.setAllowFileAccess(false);
        s.setTextZoom(100);
        // Test build: the desktop layout is still wide, so allow pinch zoom.
        s.setBuiltInZoomControls(true);
        s.setDisplayZoomControls(false);
        web.addJavascriptInterface(new Bridge(), "NocturneAndroid");

        web.setWebViewClient(new WebViewClient() {
            @Override
            public boolean shouldOverrideUrlLoading(WebView view, WebResourceRequest request) {
                Uri u = request.getUrl();
                if (isOwnPage(u)) {
                    return false;
                }
                openOutside(u);
                return true;
            }

            @Override
            public void onPageFinished(WebView view, String url) {
                if (isOwnPage(Uri.parse(url))) {
                    web.setVisibility(View.VISIBLE);
                    splash.setVisibility(View.GONE);
                }
            }

            @Override
            public boolean onRenderProcessGone(WebView view, RenderProcessGoneDetail detail) {
                // The page crashed (often: out of memory). Everything already
                // saved is safe in the library; start the window again.
                Log.w(TAG, "page process gone, crash=" + detail.didCrash());
                root.removeView(web);
                web.destroy();
                web = null;
                recreate();
                return true;
            }
        });

        web.setWebChromeClient(new WebChromeClient() {
            @Override
            public boolean onShowFileChooser(WebView view, ValueCallback<Uri[]> callback, FileChooserParams params) {
                return openFiles(callback, params);
            }

            @Override
            public boolean onConsoleMessage(ConsoleMessage m) {
                Log.d(TAG, "page: " + m.message() + " (" + m.sourceId() + ":" + m.lineNumber() + ")");
                return true;
            }
        });
    }

    private boolean isOwnPage(Uri u) {
        String scheme = u.getScheme() == null ? "" : u.getScheme();
        if (scheme.equals("blob") || scheme.equals("data") || scheme.equals("about") || scheme.equals("javascript")) {
            return true;
        }
        return server != null && "127.0.0.1".equals(u.getHost()) && u.getPort() == server.port;
    }

    private void openOutside(Uri u) {
        try {
            startActivity(new Intent(Intent.ACTION_VIEW, u));
        } catch (ActivityNotFoundException e) {
            toast("Нет приложения, чтобы открыть ссылку");
        }
    }

    // ------------------------------------------------------------ server

    private void startServer() {
        new Thread(() -> {
            try {
                LibraryServer.Info info = LibraryServer.start(this);
                runOnUiThread(() -> load(info));
            } catch (Exception e) {
                Log.e(TAG, "server start", e);
                String msg = e.getMessage() == null ? e.toString() : e.getMessage();
                runOnUiThread(() -> splash.setText("☾\n\nNOCTURNE не запустилась\n\n" + msg));
            }
        }, "nocturne-start").start();
    }

    private void load(LibraryServer.Info info) {
        if (web == null || isFinishing()) {
            return;
        }
        server = info;
        CookieManager cm = CookieManager.getInstance();
        cm.setAcceptCookie(true);
        // The server answers only requests that carry this one-time secret,
        // so other apps on the phone cannot read the library.
        cm.setCookie(info.url, "nocturne_access=" + info.token + "; Path=/; HttpOnly; SameSite=Lax", ok -> {
            cm.flush();
            if (web != null) {
                web.loadUrl(info.url);
            }
        });
    }

    // ------------------------------------------------------------ lifecycle

    @Override
    protected void onPause() {
        super.onPause();
        // Send the last unsaved keystrokes now: Android may close the app
        // at any moment while it is in the background.
        flush();
    }

    @Override
    protected void onDestroy() {
        if (web != null) {
            root.removeView(web);
            web.destroy();
            web = null;
        }
        if (isFinishing()) {
            LibraryServer.stop();
        }
        super.onDestroy();
    }

    @Override
    @SuppressWarnings("deprecation")
    public void onBackPressed() {
        // First let the page close its menu; otherwise keep the app (and
        // anything typed) as it is, like the Home button.
        if (web == null || server == null) {
            moveTaskToBack(true);
            return;
        }
        web.evaluateJavascript("window.__ncBack?window.__ncBack():false", v -> {
            if (!"true".equals(v)) {
                moveTaskToBack(true);
            }
        });
    }

    private void flush() {
        if (web != null) {
            web.evaluateJavascript("window.__nocturneSettle&&window.__nocturneSettle(8000);void 0", null);
        }
    }

    // ------------------------------------------------------------ page -> app

    private final class Bridge {
        @JavascriptInterface
        public void postMessage(String json) {
            runOnUiThread(() -> onMessage(json));
        }
    }

    private void onMessage(String json) {
        try {
            JSONObject m = new JSONObject(json);
            switch (m.optString("type")) {
                case "downloaded":
                    queueSave(m.optString("path"));
                    break;
                case "quit":
                    if (web != null) {
                        web.evaluateJavascript("window.__nocturneBeforeQuit&&window.__nocturneBeforeQuit();void 0", null);
                    }
                    break;
                case "ready-to-quit":
                    LibraryServer.stop();
                    finishAndRemoveTask();
                    break;
                case "quit-blocked":
                    toast("Не удалось безопасно закрыть: " + m.optString("message"));
                    break;
                default:
                    break;
            }
        } catch (Exception e) {
            Log.w(TAG, "bad message from page: " + json, e);
        }
    }

    // ------------------------------------------------------------ opening files

    private boolean openFiles(ValueCallback<Uri[]> callback, WebChromeClient.FileChooserParams params) {
        if (fileCallback != null) {
            fileCallback.onReceiveValue(null);
        }
        fileCallback = callback;
        Intent i = new Intent(Intent.ACTION_GET_CONTENT);
        i.addCategory(Intent.CATEGORY_OPENABLE);
        String[] types = mimeTypes(params.getAcceptTypes());
        if (types.length == 1) {
            i.setType(types[0]);
        } else {
            i.setType("*/*");
            i.putExtra(Intent.EXTRA_MIME_TYPES, types);
        }
        if (params.getMode() == WebChromeClient.FileChooserParams.MODE_OPEN_MULTIPLE) {
            i.putExtra(Intent.EXTRA_ALLOW_MULTIPLE, true);
        }
        try {
            startActivityForResult(i, REQ_OPEN);
            return true;
        } catch (ActivityNotFoundException e) {
            fileCallback = null;
            toast("Нет приложения для выбора файлов");
            return false;
        }
    }

    /**
     * Filters by type only for plain kinds like "image/*" or "audio/*". Many
     * file managers do not know .json or .ttf, so any file extension in the
     * list means "show all files".
     */
    private static String[] mimeTypes(String[] accept) {
        List<String> out = new ArrayList<>();
        if (accept != null) {
            for (String a : accept) {
                for (String t : a.split(",")) {
                    t = t.trim().toLowerCase(Locale.ROOT);
                    if (t.isEmpty()) {
                        continue;
                    }
                    if (!t.contains("/")) {
                        return new String[]{"*/*"};
                    }
                    out.add(t);
                }
            }
        }
        return out.isEmpty() ? new String[]{"*/*"} : out.toArray(new String[0]);
    }

    // ------------------------------------------------------------ saving files

    /** The server has put a file into its Downloads folder; ask where to keep it. */
    private void queueSave(String path) {
        try {
            File f = new File(path).getCanonicalFile();
            File dir = LibraryServer.downloads(this).getCanonicalFile();
            if (!dir.equals(f.getParentFile()) || !f.isFile()) {
                Log.w(TAG, "refusing to save " + path);
                return;
            }
            saveQueue.add(f);
            nextSave();
        } catch (Exception e) {
            Log.w(TAG, "save " + path, e);
        }
    }

    private void nextSave() {
        if (saving != null || saveQueue.isEmpty()) {
            return;
        }
        saving = saveQueue.poll();
        Intent i = new Intent(Intent.ACTION_CREATE_DOCUMENT);
        i.addCategory(Intent.CATEGORY_OPENABLE);
        i.setType(mimeFor(saving.getName()));
        i.putExtra(Intent.EXTRA_TITLE, saving.getName());
        try {
            startActivityForResult(i, REQ_SAVE);
        } catch (ActivityNotFoundException e) {
            toast("Нет приложения для сохранения файлов");
            finishSave(false);
        }
    }

    private void finishSave(boolean saved) {
        File f = saving;
        saving = null;
        if (f != null) {
            //noinspection ResultOfMethodCallIgnored
            f.delete();
            if (!saved) {
                toast("Файл «" + f.getName() + "» не сохранён");
            }
        }
        nextSave();
    }

    private static String mimeFor(String name) {
        String lower = name.toLowerCase(Locale.ROOT);
        int dot = lower.lastIndexOf('.');
        String ext = dot >= 0 ? lower.substring(dot + 1) : "";
        String t = MimeTypeMap.getSingleton().getMimeTypeFromExtension(ext);
        if (t != null) {
            return t;
        }
        switch (ext) {
            case "json":
                return "application/json";
            case "zip":
                return "application/zip";
            case "md":
                return "text/markdown";
            case "fb2":
                return "application/x-fictionbook+xml";
            default:
                return "application/octet-stream";
        }
    }

    @Override
    protected void onActivityResult(int requestCode, int resultCode, Intent data) {
        super.onActivityResult(requestCode, resultCode, data);
        if (requestCode == REQ_OPEN) {
            Uri[] result = null;
            if (resultCode == RESULT_OK && data != null) {
                ClipData clip = data.getClipData();
                if (clip != null && clip.getItemCount() > 0) {
                    result = new Uri[clip.getItemCount()];
                    for (int k = 0; k < result.length; k++) {
                        result[k] = clip.getItemAt(k).getUri();
                    }
                } else if (data.getData() != null) {
                    result = new Uri[]{data.getData()};
                }
            }
            if (fileCallback != null) {
                fileCallback.onReceiveValue(result);
                fileCallback = null;
            }
        } else if (requestCode == REQ_SAVE) {
            File f = saving;
            Uri dest = resultCode == RESULT_OK && data != null ? data.getData() : null;
            if (f == null || dest == null) {
                finishSave(false);
                return;
            }
            new Thread(() -> {
                boolean ok = false;
                try (InputStream in = new FileInputStream(f); OutputStream out = getContentResolver().openOutputStream(dest, "w")) {
                    if (out != null) {
                        LibraryServer.copy(in, out);
                        ok = true;
                    }
                } catch (Exception e) {
                    Log.w(TAG, "copy to " + dest, e);
                }
                boolean saved = ok;
                runOnUiThread(() -> {
                    if (saved) {
                        toast("Сохранено: «" + f.getName() + "»");
                    }
                    finishSave(saved);
                });
            }, "nocturne-save").start();
        }
    }

    private void toast(String text) {
        Toast.makeText(this, text, Toast.LENGTH_LONG).show();
    }
}
