================================================================================
 THUNDR-BOT  -  desktop app (native window, like the ChitChat reference)
================================================================================

QUICK START (কীভাবে চালাবেন)
--------------------------------------------------------------------------------
1. thundr-bot.exe  double-click করুন  ->  নিজের native window খুলবে
   (কোনো browser tab না — reference project-এর মতোই desktop app)
2. Window-এর ভেতর থেকেই সব হবে:
       [Register Accounts]  নতুন account বানাবে + settings auto-apply
       [Open Browser]       chrome খুলবে (auto)
       [Start]              bot চালু, sms folder থেকে reply, শেষে tg share
       Pages: Dashboard · Accounts · Messages · Settings · Logs

EXTRA MODE
--------------------------------------------------------------------------------
   thundr-bot.exe -server        পুরনো web dashboard চালু হয় (browser-এ
                                 http://127.0.0.1:8787) — desktop window ছাড়া
Desktop app চললে web API-ও 8787-এ চলে (backup হিসেবে)।

ONE-TIME SETUP (একবারই করতে হয় - ALREADY DONE এই মেশিনে)
--------------------------------------------------------------------------------
   powershell -ExecutionPolicy Bypass -File tools\install-chrome-task.ps1
এটা ThundrChromeWatcher task বসায় - dashboard থেকে chrome খোলার জন্য।
Task-এ auto-restart আছে (মরে গেলে ১ মিনিটে নিজেই ফিরে আসে)।

FOLDER GUIDE
--------------------------------------------------------------------------------
   thundr-bot.exe     মূল desktop app - এটাই চালাবেন
   data\              সব settings + account + session ফাইল + webview cache
   browser-profiles\  chrome profile (password/cookie সহ) - হাত দেবেন না
   tools\             chrome-watcher.ps1 + install-chrome-task.ps1
                      chrome-launch.cmd (manual fallback)
   src\               Go source (rebuild: cd src && wails build,
                      output: src\build\bin\thundr-bot.exe -> root-এ copy)
   docs\              এই ফাইল + proof-live-chat.txt (লাইভ প্রমাণ)

NOTES
--------------------------------------------------------------------------------
* Register করলেই settings auto-apply: Privacy ON, SFX ON, Video Auto-Roll
  OFF, Text Auto-Roll ON, Low Power OFF, Face Popup OFF.
* Chrome bot নিজে spawn করে না - watcher service খোলে, bot attach হয়
  (debug port 9222, settings.txt-তে chrome_attach_url)।
* thundr মাঝে মাঝে IP throttle করে (অনেক account/match হলে) - ১ ঘণ্টা
  অপেক্ষা করলে ঠিক হয়ে যায়।
================================================================================
