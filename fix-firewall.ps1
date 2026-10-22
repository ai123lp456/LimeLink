# LimeLink 防火墙规则修复脚本（v1.2.2 起：端口绑定，不再绑 exe 路径）
# 作用：
#   1. 删除旧的「按程序路径绑定」的 LimeLink 规则（目录改名/升版本后会失效，
#      历史上已两次踩坑：规则指向 limelinkv1.0 / LimeLinkv1.2.0 等旧路径）；
#   2. 新增「按端口绑定」规则：TCP 8787（HTTP/心跳/配对）+ UDP 8788（发现），
#      不绑定 exe 路径，以后再改目录、换 exe 都不用重跑本脚本。
# 用法：右键本文件 -> 「以管理员身份运行」。

$ErrorActionPreference = 'Stop'

Write-Host '========================================' -ForegroundColor Cyan
Write-Host ' LimeLink 防火墙规则修复（端口绑定版）' -ForegroundColor Cyan
Write-Host '========================================' -ForegroundColor Cyan

# 1. 删除所有历史 LimeLink 规则
Write-Host ''
Write-Host '[1/2] 清理旧的 LimeLink 防火墙规则...' -ForegroundColor Yellow
$old = Get-NetFirewallRule -ErrorAction SilentlyContinue | Where-Object { $_.DisplayName -match 'LimeLink|小青柠' }
if ($old) {
    foreach ($r in $old) {
        $prog = ($r | Get-NetFirewallApplicationFilter).Program
        Write-Host "  删除: $($r.DisplayName) -> $prog" -ForegroundColor Red
        Remove-NetFirewallRule -Name $r.Name -ErrorAction Continue
    }
    Write-Host "  已删除 $($old.Count) 条旧规则" -ForegroundColor Green
} else {
    Write-Host '  无旧规则' -ForegroundColor Gray
}

# 2. 新增端口绑定规则（不指定 -Program，任何路径的 limehost 都放行）
Write-Host ''
Write-Host '[2/2] 新增端口绑定规则...' -ForegroundColor Yellow

New-NetFirewallRule -DisplayName 'LimeLink TCP 8787' `
    -Direction Inbound -Action Allow -Protocol TCP `
    -LocalPort 8787 -Profile Domain,Private,Public | Out-Null
Write-Host '  已加规则: LimeLink TCP 8787 (HTTP/心跳/配对/记录)' -ForegroundColor Green

New-NetFirewallRule -DisplayName 'LimeLink UDP 8788' `
    -Direction Inbound -Action Allow -Protocol UDP `
    -LocalPort 8788 -Profile Domain,Private,Public | Out-Null
Write-Host '  已加规则: LimeLink UDP 8788 (广播发现)' -ForegroundColor Green

# 验证
Write-Host ''
Write-Host '当前 LimeLink 规则：' -ForegroundColor Cyan
Get-NetFirewallRule -ErrorAction SilentlyContinue | Where-Object { $_.DisplayName -match 'LimeLink' } | ForEach-Object {
    $pf = ($_ | Get-NetFirewallPortFilter)
    $app = ($_ | Get-NetFirewallApplicationFilter).Program
    $progDesc = if ($app) { $app } else { '(不限程序=端口规则)' }
    Write-Host "  $($_.DisplayName) | $($pf.Protocol) 端口 $($pf.LocalPort) | Profile $($_.Profile) | 程序 $progDesc" -ForegroundColor Gray
}

Write-Host ''
Write-Host '完成。本脚本只需跑这一次，以后目录改名/升级都不需要再跑。' -ForegroundColor Green
Read-Host '按回车键退出'
