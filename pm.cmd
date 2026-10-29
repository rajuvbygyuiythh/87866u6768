@echo off
setlocal
node "%~dp0tools\project-manager.cjs" %*
exit /b %ERRORLEVEL%
