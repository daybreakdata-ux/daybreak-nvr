package com.daybreak.nvr;

import android.annotation.SuppressLint;
import android.app.Activity;
import android.app.DownloadManager;
import android.content.Context;
import android.content.DialogInterface;
import android.content.Intent;
import android.content.res.ColorStateList;
import android.graphics.Bitmap;
import android.graphics.Color;
import android.hardware.biometrics.BiometricPrompt;
import android.net.Uri;
import android.os.Build;
import android.os.Bundle;
import android.os.CancellationSignal;
import android.os.Environment;
import android.os.Message;
import android.view.Gravity;
import android.view.View;
import android.view.ViewGroup;
import android.view.WindowManager;
import android.webkit.CookieManager;
import android.webkit.DownloadListener;
import android.webkit.URLUtil;
import android.webkit.ValueCallback;
import android.webkit.WebChromeClient;
import android.webkit.WebResourceError;
import android.webkit.WebResourceRequest;
import android.webkit.WebSettings;
import android.webkit.WebView;
import android.webkit.WebViewClient;
import android.widget.Button;
import android.widget.FrameLayout;
import android.widget.ImageButton;
import android.widget.ImageView;
import android.widget.LinearLayout;
import android.widget.ProgressBar;
import android.widget.TextView;
import android.widget.Toast;

import java.util.concurrent.Executor;

/**
 * Thin remote-only shell around the Vercel viewer. The viewer
 * handles sign-in itself against the desktop app's user accounts,
 * so this app stores no server address and no credentials — only
 * the two display/security toggles.
 */
public class MainActivity extends Activity {

    private static final String VERCEL_URL =
            "https://daybreak-nvr-daybreakdata-3224s-projects.vercel.app/";
    private static final String VERCEL_HOST =
            "daybreak-nvr-daybreakdata-3224s-projects.vercel.app";
    private static final String RELAY_HOST = "163-192-218-43.sslip.io";
    private static final int REQ_SETTINGS = 2001;
    private static final int REQ_FILE_CHOOSER = 1001;

    private static final int BG = 0xFF001119;
    private static final int CARD = 0xFF050607;
    private static final int ACCENT = 0xFF168573;
    private static final int LIME = 0xFFB2C13A;
    private static final int TEXT = 0xFFE9F1F3;
    private static final int DIM = 0xFF8BA3AD;

    private NvrPrefs prefs;
    private WebView webView;
    private ProgressBar progressBar;
    private View splash;
    private View errorOverlay;
    private TextView errorTitle;
    private TextView errorSub;
    private View lockView;
    private TextView lockStatus;
    private Button lockContinueWithout;
    private ImageButton settingsButton;
    private FrameLayout rootLayout;

    private View customView;
    private WebChromeClient.CustomViewCallback customViewCallback;
    private ValueCallback<Uri[]> filePathCallback;
    private CancellationSignal biometricCancel;

    private boolean pageLoadedOnce = false;
    private boolean mainFrameFailed = false;
    private boolean unlockedThisSession = false;
    private String currentTarget;

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        prefs = new NvrPrefs(this);
        getWindow().setStatusBarColor(BG);
        getWindow().setNavigationBarColor(BG);
        buildUi();
        configureWebView();
        if (savedInstanceState != null) {
            webView.restoreState(savedInstanceState);
            pageLoadedOnce = true;
            splash.setVisibility(View.GONE);
        } else {
            startFlow();
        }
    }

    // ---------- startup flow ----------

    private void startFlow() {
        applyKeepScreenOn();
        if (prefs.isBiometricEnabled() && !unlockedThisSession) {
            showLockView();
            return;
        }
        loadTarget();
    }

    private void applyKeepScreenOn() {
        if (prefs.isKeepScreenOn()) {
            getWindow().addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON);
        } else {
            getWindow().clearFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON);
        }
    }

    private void loadTarget() {
        currentTarget = VERCEL_URL;
        mainFrameFailed = false;
        errorOverlay.setVisibility(View.GONE);
        webView.loadUrl(VERCEL_URL);
    }

    private void openSettings() {
        Intent i = new Intent(this, SettingsActivity.class);
        startActivityForResult(i, REQ_SETTINGS);
    }

    @Override
    protected void onActivityResult(int requestCode, int resultCode, Intent data) {
        super.onActivityResult(requestCode, resultCode, data);
        if (requestCode == REQ_FILE_CHOOSER) {
            if (filePathCallback != null) {
                filePathCallback.onReceiveValue(
                        WebChromeClient.FileChooserParams.parseResult(resultCode, data));
                filePathCallback = null;
            }
            return;
        }
        if (requestCode == REQ_SETTINGS) {
            applyKeepScreenOn();
            if (prefs.isBiometricEnabled() && !unlockedThisSession) {
                showLockView();
            } else {
                loadTarget();
            }
        }
    }

    // ---------- biometric gate ----------

    private void showLockView() {
        lockStatus.setText("");
        lockContinueWithout.setVisibility(View.GONE);
        lockView.setVisibility(View.VISIBLE);
        splash.setVisibility(View.GONE);
    }

    private void authenticate() {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.P) {
            lockStatus.setText("Fingerprint or face unlock needs Android 9 or later.");
            lockContinueWithout.setVisibility(View.VISIBLE);
            return;
        }
        Executor executor = getMainExecutor();
        BiometricPrompt.Builder builder = new BiometricPrompt.Builder(this)
                .setTitle("Unlock")
                .setDescription("Confirm it is you to open the viewer.");
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) {
            builder.setAllowedAuthenticators(
                    android.hardware.biometrics.BiometricManager.Authenticators.BIOMETRIC_STRONG);
        }
        builder.setNegativeButton("Cancel", executor,
                new DialogInterface.OnClickListener() {
                    @Override
                    public void onClick(DialogInterface dialog, int which) {
                        // Stay on the lock screen.
                    }
                });
        BiometricPrompt prompt = builder.build();
        biometricCancel = new CancellationSignal();
        prompt.authenticate(biometricCancel, executor,
                new BiometricPrompt.AuthenticationCallback() {
                    @Override
                    public void onAuthenticationError(int errorCode, CharSequence errString) {
                        if (errorCode == BiometricPrompt.BIOMETRIC_ERROR_NO_BIOMETRICS
                                || errorCode == BiometricPrompt.BIOMETRIC_ERROR_HW_UNAVAILABLE
                                || errorCode == BiometricPrompt.BIOMETRIC_ERROR_NO_DEVICE_CREDENTIAL
                                || errorCode == BiometricPrompt.BIOMETRIC_ERROR_HW_NOT_PRESENT) {
                            lockStatus.setText("No fingerprint or face unlock is set up on this phone.");
                            lockContinueWithout.setVisibility(View.VISIBLE);
                        } else if (errorCode != BiometricPrompt.BIOMETRIC_ERROR_USER_CANCELED
                                && errorCode != BiometricPrompt.BIOMETRIC_ERROR_CANCELED) {
                            lockStatus.setText(String.valueOf(errString));
                        }
                    }

                    @Override
                    public void onAuthenticationFailed() {
                        lockStatus.setText("Not recognized — try again.");
                    }

                    @Override
                    public void onAuthenticationSucceeded(
                            BiometricPrompt.AuthenticationResult result) {
                        unlockedThisSession = true;
                        lockView.setVisibility(View.GONE);
                        loadTarget();
                    }
                });
    }

    // ---------- UI ----------

    private void buildUi() {
        rootLayout = new FrameLayout(this);
        rootLayout.setBackgroundColor(BG);

        webView = new WebView(this);
        rootLayout.addView(webView, new FrameLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT,
                ViewGroup.LayoutParams.MATCH_PARENT));

        progressBar = new ProgressBar(this, null,
                android.R.attr.progressBarStyleHorizontal);
        progressBar.setMax(100);
        progressBar.setProgressTintList(ColorStateList.valueOf(LIME));
        progressBar.setVisibility(View.GONE);
        rootLayout.addView(progressBar, new FrameLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT, dp(3)));

        settingsButton = new ImageButton(this);
        settingsButton.setImageResource(android.R.drawable.ic_menu_preferences);
        settingsButton.setBackgroundColor(Color.TRANSPARENT);
        settingsButton.setAlpha(0.85f);
        settingsButton.setContentDescription("Settings");
        settingsButton.setOnClickListener(new View.OnClickListener() {
            @Override
            public void onClick(View v) {
                openSettings();
            }
        });
        FrameLayout.LayoutParams gearParams = new FrameLayout.LayoutParams(dp(48), dp(48));
        gearParams.gravity = Gravity.TOP | Gravity.END;
        gearParams.setMargins(0, dp(8), dp(8), 0);
        rootLayout.addView(settingsButton, gearParams);

        splash = buildSplash();
        rootLayout.addView(splash);

        errorOverlay = buildErrorOverlay();
        errorOverlay.setVisibility(View.GONE);
        rootLayout.addView(errorOverlay);

        lockView = buildLockView();
        lockView.setVisibility(View.GONE);
        rootLayout.addView(lockView);

        setContentView(rootLayout);
    }

    private View buildSplash() {
        LinearLayout layout = new LinearLayout(this);
        layout.setOrientation(LinearLayout.VERTICAL);
        layout.setGravity(Gravity.CENTER);
        layout.setBackgroundColor(BG);
        ImageView icon = new ImageView(this);
        icon.setImageResource(R.mipmap.ic_launcher);
        layout.addView(icon, new LinearLayout.LayoutParams(dp(120), dp(120)));
        return layout;
    }

    private View buildErrorOverlay() {
        LinearLayout layout = new LinearLayout(this);
        layout.setOrientation(LinearLayout.VERTICAL);
        layout.setGravity(Gravity.CENTER);
        layout.setBackgroundColor(BG);
        layout.setPadding(dp(32), 0, dp(32), 0);
        errorTitle = new TextView(this);
        errorTitle.setText("Can't reach the server");
        errorTitle.setTextColor(TEXT);
        errorTitle.setTextSize(20f);
        errorTitle.setGravity(Gravity.CENTER);
        layout.addView(errorTitle);
        errorSub = new TextView(this);
        errorSub.setText("Check your connection, then try again.");
        errorSub.setTextColor(DIM);
        errorSub.setTextSize(14f);
        errorSub.setGravity(Gravity.CENTER);
        errorSub.setPadding(0, dp(8), 0, dp(20));
        layout.addView(errorSub);
        LinearLayout row = new LinearLayout(this);
        row.setOrientation(LinearLayout.HORIZONTAL);
        row.setGravity(Gravity.CENTER);
        Button retry = new Button(this);
        retry.setText("Retry");
        retry.setOnClickListener(new View.OnClickListener() {
            @Override
            public void onClick(View v) {
                errorOverlay.setVisibility(View.GONE);
                mainFrameFailed = false;
                if (currentTarget == null) {
                    loadTarget();
                } else {
                    webView.reload();
                }
            }
        });
        row.addView(retry, new LinearLayout.LayoutParams(dp(130), dp(52)));
        Button settings = new Button(this);
        settings.setText("Settings");
        settings.setOnClickListener(new View.OnClickListener() {
            @Override
            public void onClick(View v) {
                openSettings();
            }
        });
        LinearLayout.LayoutParams sp = new LinearLayout.LayoutParams(dp(130), dp(52));
        sp.setMargins(dp(12), 0, 0, 0);
        row.addView(settings, sp);
        layout.addView(row);
        return layout;
    }

    private View buildLockView() {
        LinearLayout layout = new LinearLayout(this);
        layout.setOrientation(LinearLayout.VERTICAL);
        layout.setGravity(Gravity.CENTER);
        layout.setBackgroundColor(BG);
        layout.setPadding(dp(32), 0, dp(32), 0);
        ImageView icon = new ImageView(this);
        icon.setImageResource(R.mipmap.ic_launcher);
        layout.addView(icon, new LinearLayout.LayoutParams(dp(96), dp(96)));
        lockStatus = new TextView(this);
        lockStatus.setTextColor(DIM);
        lockStatus.setTextSize(14f);
        lockStatus.setGravity(Gravity.CENTER);
        lockStatus.setPadding(0, dp(16), 0, dp(20));
        layout.addView(lockStatus);
        Button unlock = new Button(this);
        unlock.setText("Unlock");
        unlock.setOnClickListener(new View.OnClickListener() {
            @Override
            public void onClick(View v) {
                authenticate();
            }
        });
        layout.addView(unlock, new LinearLayout.LayoutParams(dp(180), dp(52)));
        lockContinueWithout = new Button(this);
        lockContinueWithout.setText("Continue without unlock");
        lockContinueWithout.setVisibility(View.GONE);
        lockContinueWithout.setOnClickListener(new View.OnClickListener() {
            @Override
            public void onClick(View v) {
                prefs.setBiometricEnabled(false);
                Toast.makeText(MainActivity.this,
                        "Unlock requirement turned off.", Toast.LENGTH_SHORT).show();
                unlockedThisSession = true;
                lockView.setVisibility(View.GONE);
                loadTarget();
            }
        });
        LinearLayout.LayoutParams cp =
                new LinearLayout.LayoutParams(dp(220), dp(52));
        cp.setMargins(0, dp(12), 0, 0);
        layout.addView(lockContinueWithout, cp);
        return layout;
    }

    private void showError(String title, String sub) {
        errorTitle.setText(title);
        errorSub.setText(sub);
        splash.setVisibility(View.GONE);
        errorOverlay.setVisibility(View.VISIBLE);
    }

    private int dp(int value) {
        return (int) (value * getResources().getDisplayMetrics().density);
    }

    // ---------- WebView ----------

    private int currentVersionCode() {
        try {
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.P) {
                return (int) getPackageManager()
                        .getPackageInfo(getPackageName(), 0).getLongVersionCode();
            }
            return getPackageManager()
                    .getPackageInfo(getPackageName(), 0).versionCode;
        } catch (Exception e) {
            return 0;
        }
    }

    @SuppressLint("SetJavaScriptEnabled")
    private void configureWebView() {
        // Fresh viewer on each app update: the WebView cache can hold a
        // stale viewer (old service worker / app.js) across updates, so
        // clip thumbnails and other viewer fixes never show up. Clear it
        // once per version, before the first load. This clears the HTTP
        // cache only — the viewer's sign-in (localStorage) and cookies
        // are kept, so he stays signed in.
        int vc = currentVersionCode();
        if (vc != 0 && prefs.getCacheClearedVersion() != vc) {
            webView.clearCache(true);
            prefs.setCacheClearedVersion(vc);
        }

        WebSettings s = webView.getSettings();
        s.setJavaScriptEnabled(true);
        s.setDomStorageEnabled(true);
        s.setDatabaseEnabled(true);
        s.setMediaPlaybackRequiresUserGesture(false);
        s.setLoadWithOverviewMode(true);
        s.setUseWideViewPort(true);
        s.setSupportZoom(true);
        s.setBuiltInZoomControls(false);
        s.setDisplayZoomControls(false);
        s.setSupportMultipleWindows(true);
        s.setCacheMode(WebSettings.LOAD_DEFAULT);

        CookieManager cookieManager = CookieManager.getInstance();
        cookieManager.setAcceptCookie(true);
        cookieManager.setAcceptThirdPartyCookies(webView, true);

        webView.setWebViewClient(new WebViewClient() {
            @Override
            public boolean shouldOverrideUrlLoading(WebView view, WebResourceRequest request) {
                return handleNavigation(request.getUrl());
            }

            @Override
            @SuppressWarnings("deprecation")
            public boolean shouldOverrideUrlLoading(WebView view, String url) {
                return handleNavigation(Uri.parse(url));
            }

            @Override
            public void onPageStarted(WebView view, String url, Bitmap favicon) {
                mainFrameFailed = false;
            }

            @Override
            public void onPageFinished(WebView view, String url) {
                if (!mainFrameFailed) {
                    errorOverlay.setVisibility(View.GONE);
                }
                if (!pageLoadedOnce) {
                    pageLoadedOnce = true;
                    splash.animate().alpha(0f).setDuration(250)
                            .withEndAction(new Runnable() {
                                @Override
                                public void run() {
                                    splash.setVisibility(View.GONE);
                                }
                            }).start();
                }
            }

            @Override
            public void onReceivedError(WebView view, WebResourceRequest request,
                                        WebResourceError error) {
                if (request.isForMainFrame()) {
                    mainFrameFailed = true;
                    splash.setVisibility(View.GONE);
                    showError("Can't reach the server",
                            "Check your connection, then try again.");
                }
            }
        });

        webView.setWebChromeClient(new WebChromeClient() {
            @Override
            public void onProgressChanged(WebView view, int newProgress) {
                progressBar.setProgress(newProgress);
                progressBar.setVisibility(
                        (newProgress > 0 && newProgress < 100) ? View.VISIBLE : View.GONE);
            }

            @Override
            public void onShowCustomView(View view, CustomViewCallback callback) {
                if (view == null || callback == null) return;
                if (customView != null) {
                    callback.onCustomViewHidden();
                    return;
                }
                customView = view;
                customViewCallback = callback;
                rootLayout.addView(view, new FrameLayout.LayoutParams(
                        ViewGroup.LayoutParams.MATCH_PARENT,
                        ViewGroup.LayoutParams.MATCH_PARENT));
                webView.setVisibility(View.INVISIBLE);
                settingsButton.setVisibility(View.GONE);
                getWindow().getDecorView().setSystemUiVisibility(
                        View.SYSTEM_UI_FLAG_FULLSCREEN
                                | View.SYSTEM_UI_FLAG_HIDE_NAVIGATION
                                | View.SYSTEM_UI_FLAG_IMMERSIVE_STICKY
                                | View.SYSTEM_UI_FLAG_LAYOUT_FULLSCREEN
                                | View.SYSTEM_UI_FLAG_LAYOUT_HIDE_NAVIGATION
                                | View.SYSTEM_UI_FLAG_LAYOUT_STABLE);
            }

            @Override
            public void onHideCustomView() {
                hideCustomView();
            }

            @Override
            public boolean onCreateWindow(WebView view, boolean isDialog,
                                          boolean isUserGesture, Message resultMsg) {
                WebView popup = new WebView(MainActivity.this);
                popup.setWebViewClient(new WebViewClient() {
                    @Override
                    public boolean shouldOverrideUrlLoading(
                            WebView v, WebResourceRequest request) {
                        webView.loadUrl(request.getUrl().toString());
                        return true;
                    }
                });
                WebView.WebViewTransport transport =
                        (WebView.WebViewTransport) resultMsg.obj;
                transport.setWebView(popup);
                resultMsg.sendToTarget();
                return true;
            }

            @Override
            public boolean onShowFileChooser(WebView webView,
                                             ValueCallback<Uri[]> filePathCallback,
                                             FileChooserParams fileChooserParams) {
                if (MainActivity.this.filePathCallback != null) {
                    MainActivity.this.filePathCallback.onReceiveValue(null);
                }
                MainActivity.this.filePathCallback = filePathCallback;
                Intent intent = fileChooserParams.createIntent();
                try {
                    startActivityForResult(intent, REQ_FILE_CHOOSER);
                    return true;
                } catch (Exception e) {
                    MainActivity.this.filePathCallback = null;
                    return false;
                }
            }
        });

        webView.setDownloadListener(new DownloadListener() {
            @Override
            public void onDownloadStart(String url, String userAgent,
                                        String contentDisposition, String mimeType,
                                        long contentLength) {
                startDownload(url, userAgent, contentDisposition, mimeType);
            }
        });
    }

    private boolean handleNavigation(Uri uri) {
        String scheme = uri.getScheme() == null ? "" : uri.getScheme().toLowerCase();
        if (!"http".equals(scheme) && !"https".equals(scheme)) {
            try {
                startActivity(new Intent(Intent.ACTION_VIEW, uri));
            } catch (Exception ignored) {
            }
            return true;
        }
        String host = uri.getHost() == null ? "" : uri.getHost().toLowerCase();
        if (isInAppHost(host)) {
            return false; // Let the WebView load it.
        }
        try {
            startActivity(new Intent(Intent.ACTION_VIEW, uri));
        } catch (Exception ignored) {
        }
        return true;
    }

    private boolean isInAppHost(String host) {
        if (host.isEmpty()) return false;
        return host.equals(VERCEL_HOST) || host.endsWith("." + VERCEL_HOST)
                || host.equals(RELAY_HOST) || host.endsWith("." + RELAY_HOST);
    }

    private void startDownload(String url, String userAgent,
                               String contentDisposition, String mimeType) {
        if (url == null || url.isEmpty()) return;
        try {
            DownloadManager.Request request =
                    new DownloadManager.Request(Uri.parse(url));
            request.setMimeType(mimeType);
            request.addRequestHeader("User-Agent", userAgent == null ? "" : userAgent);
            String cookie = CookieManager.getInstance().getCookie(url);
            if (cookie != null) {
                request.addRequestHeader("Cookie", cookie);
            }
            String fileName = URLUtil.guessFileName(url, contentDisposition, mimeType);
            request.setDestinationInExternalPublicDir(
                    Environment.DIRECTORY_DOWNLOADS, fileName);
            request.setNotificationVisibility(
                    DownloadManager.Request.VISIBILITY_VISIBLE_NOTIFY_COMPLETED);
            request.setTitle(fileName);
            DownloadManager dm =
                    (DownloadManager) getSystemService(Context.DOWNLOAD_SERVICE);
            dm.enqueue(request);
            Toast.makeText(this, "Downloading — check your Downloads folder",
                    Toast.LENGTH_SHORT).show();
        } catch (Exception e) {
            try {
                startActivity(new Intent(Intent.ACTION_VIEW, Uri.parse(url)));
            } catch (Exception ignored) {
            }
        }
    }

    private void hideCustomView() {
        if (customView == null) return;
        rootLayout.removeView(customView);
        customView = null;
        webView.setVisibility(View.VISIBLE);
        settingsButton.setVisibility(View.VISIBLE);
        getWindow().getDecorView().setSystemUiVisibility(View.SYSTEM_UI_FLAG_VISIBLE);
        if (customViewCallback != null) {
            customViewCallback.onCustomViewHidden();
            customViewCallback = null;
        }
    }

    // ---------- lifecycle ----------

    @Override
    @SuppressWarnings("deprecation")
    public void onBackPressed() {
        if (customView != null) {
            hideCustomView();
        } else if (webView.canGoBack()) {
            webView.goBack();
        } else {
            super.onBackPressed();
        }
    }

    @Override
    protected void onSaveInstanceState(Bundle outState) {
        super.onSaveInstanceState(outState);
        webView.saveState(outState);
    }

    @Override
    protected void onPause() {
        webView.onPause();
        super.onPause();
    }

    @Override
    protected void onResume() {
        super.onResume();
        webView.onResume();
    }

    @Override
    protected void onDestroy() {
        if (biometricCancel != null) {
            biometricCancel.cancel();
        }
        rootLayout.removeView(webView);
        webView.destroy();
        super.onDestroy();
    }
}
