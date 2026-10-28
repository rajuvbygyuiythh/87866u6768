@echo off
setlocal
cd /d "%~dp0src"
go run . -server -no-browser -data "%~dp0data"
pause
