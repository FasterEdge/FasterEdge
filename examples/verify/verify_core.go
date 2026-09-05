package main

import (
	"fmt"
	"time"

	"github.com/FasterEdge/FasterEdge/ability"
	"github.com/FasterEdge/FasterEdge/data"
	"github.com/FasterEdge/FasterEdge/types"
)

// verifyCoreExtras 覆盖核心能力(Cmd/Sh/Bash 进程管理 + Time/TSN/OneKey/Keyring/
// Cloud/Edge)的命令盲区——第十三轮全仓命令枚举 mapping 发现。
// 纪律与主段一致: 类型断言失败 + err==nil = FAIL; 参数校验/空状态/无依赖的
// "正确拒绝"与值段(PASS)。所有段无真实 shell/网络/设备依赖。
func verifyCoreExtras(atom, extAtom *types.Atom) {
	// --- CmdAbility: kill/list/get_job/wait/clear_jobs 空状态语义 ---
	if a, ok := extAtom.Ability("CmdAbility"); ok {
		o := a.Command(extAtom, ability.CmdCommandKill, ability.CmdKillArgs{JobID: "job-none"})
		if o.Err == nil {
			report("Cmd/kill-missing", "应拒绝但成功", fmt.Errorf("kill on missing job accepted"))
		} else {
			report("Cmd/kill-missing", "正确拒绝", nil)
		}
		o = a.Command(extAtom, ability.CmdCommandGetJob, ability.CmdJobIDArg{JobID: "job-none"})
		if o.Err == nil {
			report("Cmd/get_job-missing", "应拒绝但成功", fmt.Errorf("get_job on missing job accepted"))
		} else {
			report("Cmd/get_job-missing", "正确拒绝", nil)
		}
		o = a.Command(extAtom, ability.CmdCommandWait, ability.CmdWaitArgs{JobID: "job-none", Wait: time.Millisecond})
		if o.Err == nil {
			report("Cmd/wait-missing", "应拒绝但成功", fmt.Errorf("wait on missing job accepted"))
		} else {
			report("Cmd/wait-missing", "正确拒绝", nil)
		}
		o = a.Command(extAtom, ability.CmdCommandList, nil)
		if jobs, ok := o.Value.([]ability.CmdJob); ok {
			report("Cmd/list", fmt.Sprintf("jobs=%d", len(jobs)), nil)
		} else if o.Err != nil {
			report("Cmd/list", fmt.Sprintf("%v", o.Value), o.Err)
		} else {
			report("Cmd/list", fmt.Sprintf("%T", o.Value), fmt.Errorf("list returned %T (want []ability.CmdJob)", o.Value))
		}
		o = a.Command(extAtom, ability.CmdCommandClearJobs, nil)
		report("Cmd/clear_jobs", fmt.Sprintf("%v", o.Value), o.Err)
		// start 类型错 → 拒绝
		o = a.Command(extAtom, ability.CmdCommandStart, "raw")
		if o.Err == nil {
			report("Cmd/start-type", "应拒绝但成功", fmt.Errorf("wrong type accepted"))
		} else {
			report("Cmd/start-type", "正确拒绝", nil)
		}
		// get_allowlist 往返(main 段在 atom 上 set echo——extAtom 实例独立,
		// 本段自行 set 再断言, 覆盖 set→get 往返)
		o = a.Command(extAtom, ability.CmdCommandSetAllowlist, ability.CmdSetAllowlistArgs{Entries: []ability.CmdAllowlistEntry{{Name: "echo"}}})
		report("Cmd/set_allowlist2", "echo", o.Err)
		o = a.Command(extAtom, ability.CmdCommandGetAllowlist, nil)
		if entries, ok := o.Value.([]ability.CmdAllowlistEntry); ok {
			found := false
			for _, e := range entries {
				if e.Name == "echo" {
					found = true
					break
				}
			}
			if !found {
				report("Cmd/get_allowlist", fmt.Sprintf("%v", entries), fmt.Errorf("echo not listed after set_allowlist"))
			} else {
				report("Cmd/get_allowlist", fmt.Sprintf("entries=%d", len(entries)), nil)
			}
		} else if o.Err != nil {
			report("Cmd/get_allowlist", fmt.Sprintf("%v", o.Value), o.Err)
		} else {
			report("Cmd/get_allowlist", fmt.Sprintf("%T", o.Value), fmt.Errorf("get_allowlist returned %T (want []ability.CmdAllowlistEntry)", o.Value))
		}
	}

	// --- ShAbility/BashAbility: set/get_allowlist 往返 + start/kill/list/wait 盲区 ---
	// 注意: main 段的 Sh/Bash 在 atom 上 set allowlist——extAtom 实例独立,
	// 本段自行 set 再断言(往返), 不依赖 main 段的状态。
	for _, pair := range []struct {
		name, getCmd string
		allowed      []string
	}{
		{"ShAbility", ability.ShCommandGetAllowlist, []string{"printf"}},
		{"BashAbility", ability.BashCommandGetAllowlist, []string{"echo", "printf"}},
	} {
		if a, ok := extAtom.Ability(pair.name); ok {
			o := a.Command(extAtom, mapBashToSh(pair.name, ability.ShCommandSetAllowlist), ability.ShSetAllowlistArgs{Allowed: pair.allowed})
			report(pair.name+"/set_allowlist2", fmt.Sprintf("%v", pair.allowed), o.Err)
			o = a.Command(extAtom, pair.getCmd, nil)
			if al, ok := o.Value.(ability.ShAllowlist); ok {
				if len(al.Allowed) != len(pair.allowed) {
					report(pair.name+"/get_allowlist", fmt.Sprintf("%+v", al), fmt.Errorf("set_allowlist not reflected: %+v", al))
				} else {
					report(pair.name+"/get_allowlist", fmt.Sprintf("shell=%s entries=%d", al.Shell, len(al.Allowed)), nil)
				}
			} else if o.Err != nil {
				report(pair.name+"/get_allowlist", fmt.Sprintf("%v", o.Value), o.Err)
			} else {
				report(pair.name+"/get_allowlist", fmt.Sprintf("%T", o.Value), fmt.Errorf("get_allowlist returned %T (want ability.ShAllowlist)", o.Value))
			}
			// kill 不存在 job → 拒绝(委托 Cmd 层)
			o = a.Command(extAtom, mapBashToSh(pair.name, ability.ShCommandKill), ability.ShKillArgs{JobID: "job-none"})
			if o.Err == nil {
				report(pair.name+"/kill-missing", "应拒绝但成功", fmt.Errorf("kill on missing job accepted"))
			} else {
				report(pair.name+"/kill-missing", "正确拒绝", nil)
			}
			// wait 不存在 job → 拒绝(快速返回, 无挂起)
			o = a.Command(extAtom, mapBashToSh(pair.name, ability.ShCommandWait), ability.ShWaitArgs{JobID: "job-none", Wait: time.Millisecond})
			if o.Err == nil {
				report(pair.name+"/wait-missing", "应拒绝但成功", fmt.Errorf("wait on missing job accepted"))
			} else {
				report(pair.name+"/wait-missing", "正确拒绝", nil)
			}
			// list 值段(委托 Cmd 层)
			o = a.Command(extAtom, mapBashToSh(pair.name, ability.ShCommandList), nil)
			if jobs, ok := o.Value.([]ability.CmdJob); ok {
				report(pair.name+"/list", fmt.Sprintf("jobs=%d", len(jobs)), nil)
			} else if o.Err != nil {
				report(pair.name+"/list", fmt.Sprintf("%v", o.Value), o.Err)
			} else {
				report(pair.name+"/list", fmt.Sprintf("%T", o.Value), fmt.Errorf("list returned %T (want []ability.CmdJob)", o.Value))
			}
			// start 白名单外命令 → 拒绝(matchInner/checkInner 在 exec 前拦截)
			o = a.Command(extAtom, mapBashToSh(pair.name, ability.ShCommandStart), ability.ShRunArgs{Command: "touch /tmp/fe-unauth", Timeout: time.Second})
			if o.Err == nil {
				report(pair.name+"/start-deny", "应拒绝但成功", fmt.Errorf("non-allowlisted command accepted"))
			} else {
				report(pair.name+"/start-deny", "正确拒绝", nil)
			}
		}
	}

	// Bash set_allowlist 往返(独立于上面循环——Bash 的 set 与 Sh 参数同构)
	if a, ok := extAtom.Ability("BashAbility"); ok {
		o := a.Command(extAtom, ability.BashCommandSetAllowlist, ability.ShSetAllowlistArgs{Allowed: []string{"echo", "printf"}})
		if al, ok := o.Value.(ability.ShAllowlist); ok {
			if len(al.Allowed) != 2 || al.Allowed[0] != "echo" || al.Allowed[1] != "printf" {
				report("Bash/set_allowlist", fmt.Sprintf("%+v", al), fmt.Errorf("set_allowlist not reflected: %+v", al))
			} else {
				report("Bash/set_allowlist", fmt.Sprintf("%v", al.Allowed), nil)
			}
		} else if o.Err != nil {
			report("Bash/set_allowlist", fmt.Sprintf("%v", o.Value), o.Err)
		} else {
			report("Bash/set_allowlist", fmt.Sprintf("%T", o.Value), fmt.Errorf("set_allowlist returned %T (want ability.ShAllowlist)", o.Value))
		}
	}

	// --- TimeAbility: configure_run 模式校验(合法往返 + 非法拒绝) ---
	if a, ok := extAtom.Ability("TimeAbility"); ok {
		o := a.Command(extAtom, ability.TimeCommandConfigureRun, ability.TimeConfigureRunArgs{Mode: ability.TimeRunModeMonotonic, Interval: 0})
		report("Time/configure_run-monotonic", "monotonic", o.Err)
		o = a.Command(extAtom, ability.TimeCommandConfigureRun, ability.TimeConfigureRunArgs{Mode: ability.TimeRunModeTicker, Interval: time.Second})
		report("Time/configure_run-ticker", "ticker 1s", o.Err)
		o = a.Command(extAtom, ability.TimeCommandConfigureRun, ability.TimeConfigureRunArgs{Mode: "bogus", Interval: time.Second})
		if o.Err == nil {
			report("Time/configure_run-bad-mode", "应拒绝但成功", fmt.Errorf("unknown mode accepted"))
		} else {
			report("Time/configure_run-bad-mode", "正确拒绝", nil)
		}
		o = a.Command(extAtom, ability.TimeCommandConfigureRun, ability.TimeConfigureRunArgs{Mode: ability.TimeRunModeMonotonic, Interval: time.Second})
		if o.Err == nil {
			report("Time/configure_run-monotonic-interval", "应拒绝但成功", fmt.Errorf("monotonic with interval accepted"))
		} else {
			report("Time/configure_run-monotonic-interval", "正确拒绝", nil)
		}
		// last_sync 值段(TimeSnapshot 快照)
		o = a.Command(extAtom, ability.TimeCommandLastSync, nil)
		if snap, ok := o.Value.(ability.TimeSnapshot); ok {
			report("Time/last_sync", fmt.Sprintf("source=%q", snap.Source), nil)
		} else if o.Err != nil {
			report("Time/last_sync", fmt.Sprintf("%v", o.Value), o.Err)
		} else {
			report("Time/last_sync", fmt.Sprintf("%T", o.Value), fmt.Errorf("last_sync returned %T (want ability.TimeSnapshot)", o.Value))
		}
	}

	// --- TSNAbility: get_priority_map 往返 + unregister 拒绝/往返 ---
	if a, ok := extAtom.Ability("TSNAbility"); ok {
		o := a.Command(extAtom, ability.TSNCommandGetPriority, nil)
		if m, ok := o.Value.(map[uint8]uint8); ok {
			report("TSN/get_priority_map", fmt.Sprintf("entries=%d", len(m)), nil)
		} else if o.Err != nil {
			report("TSN/get_priority_map", fmt.Sprintf("%v", o.Value), o.Err)
		} else {
			report("TSN/get_priority_map", fmt.Sprintf("%T", o.Value), fmt.Errorf("get_priority_map returned %T (want map[uint8]uint8)", o.Value))
		}
		// register_listener 往返(现有段已 register talker)
		o = a.Command(extAtom, ability.TSNCommandRegisterListener, ability.TSNRegisterListenerArgs{ID: "fe-verify-listener", MAC: "02:00:00:00:00:02", DestMAC: "01:00:5E:00:00:01", VLANID: 1, Priority: 3})
		report("TSN/register_listener", "fe-verify-listener", o.Err)
		o = a.Command(extAtom, ability.TSNCommandUnregister, ability.TSNStreamIDArg{ID: "fe-verify-listener"})
		report("TSN/unregister", "fe-verify-listener", o.Err)
		o = a.Command(extAtom, ability.TSNCommandUnregister, ability.TSNStreamIDArg{ID: "fe-no-such-stream"})
		if o.Err == nil {
			report("TSN/unregister-missing", "应拒绝但成功", fmt.Errorf("unregister missing stream accepted"))
		} else {
			report("TSN/unregister-missing", "正确拒绝", nil)
		}
		// get_time_aware 往返(main 段已 set_time_aware enabled=true)
		o = a.Command(extAtom, ability.TSNCommandGetTimeAware, nil)
		if ta, ok := o.Value.(ability.TSNTimeAwareArgs); ok {
			report("TSN/get_time_aware", fmt.Sprintf("enabled=%v", ta.Enabled), nil)
		} else if o.Err != nil {
			report("TSN/get_time_aware", fmt.Sprintf("%v", o.Value), o.Err)
		} else {
			report("TSN/get_time_aware", fmt.Sprintf("%T", o.Value), fmt.Errorf("get_time_aware returned %T (want ability.TSNTimeAwareArgs)", o.Value))
		}
	}

	// --- OneKeyAbility: rotate/status/revoke_all 盲区(签发→rotate→旧签名失效) ---
	if a, ok := extAtom.Ability("OneKeyAbility"); ok {
		o := a.Command(extAtom, ability.OneKeyCommandIssueToken, ability.OneKeyIssueTokenArgs{Subject: "fe-core-rotate", TTL: time.Hour})
		if o.Err != nil {
			report("OneKey/rotate-issue", fmt.Sprintf("%v", o.Err), o.Err)
		} else if tok, ok := o.Value.(ability.OneKeyToken); ok {
			report("OneKey/rotate-issue", "fe-core-rotate", nil)
			o = a.Command(extAtom, ability.OneKeyCommandRotate, nil)
			report("OneKey/rotate", fmt.Sprintf("%v", o.Value), o.Err)
			// rotate 后旧签名必须失效(第九轮 verify 同型断言——本段覆盖 OneKey 直接层)
			o = a.Command(extAtom, ability.OneKeyCommandVerifyToken, ability.OneKeyVerifyTokenArgs{
				Subject: tok.Subject, IssuedAt: tok.IssuedAt, ExpiresAt: tok.ExpiresAt, Signature: tok.Signature,
			})
			if o.Err == nil {
				report("OneKey/rotate-invalidates-issued", "旧令牌仍有效", fmt.Errorf("old token still verifies after rotate"))
			} else {
				report("OneKey/rotate-invalidates-issued", "旧签名失效", nil)
			}
		} else if o.Err != nil {
			report("OneKey/rotate-issue", fmt.Sprintf("%v", o.Value), o.Err)
		} else {
			report("OneKey/rotate-issue", fmt.Sprintf("%T", o.Value), fmt.Errorf("issue returned %T (want ability.OneKeyToken)", o.Value))
		}
		o = a.Command(extAtom, ability.OneKeyCommandStatus, nil)
		if st, ok := o.Value.(data.KeyringStatus); ok {
			report("OneKey/status", fmt.Sprintf("active=%d issued=%d", st.ActiveTokens, st.TotalIssued), nil)
		} else if o.Err != nil {
			report("OneKey/status", fmt.Sprintf("%v", o.Value), o.Err)
		} else {
			report("OneKey/status", fmt.Sprintf("%T", o.Value), fmt.Errorf("status returned %T (want data.KeyringStatus)", o.Value))
		}
		o = a.Command(extAtom, ability.OneKeyCommandRevokeAll, nil)
		report("OneKey/revoke_all", fmt.Sprintf("%v", o.Value), o.Err)
		o = a.Command(extAtom, ability.OneKeyCommandListTokens, nil)
		if tokens, ok := o.Value.([]data.KeyringToken); ok {
			if len(tokens) != 0 {
				report("OneKey/revoke_all-cleared", fmt.Sprintf("tokens=%d", len(tokens)), fmt.Errorf("tokens remain after revoke_all"))
			} else {
				report("OneKey/revoke_all-cleared", "empty", nil)
			}
		} else if o.Err != nil {
			report("OneKey/revoke_all-cleared", fmt.Sprintf("%v", o.Value), o.Err)
		} else {
			report("OneKey/revoke_all-cleared", fmt.Sprintf("%T", o.Value), fmt.Errorf("list_tokens returned %T (want []data.KeyringToken)", o.Value))
		}
	}

	// --- KeyringData: revoke_token 直接层往返(签发→吊销→验证失败) ---
	if d, ok := atom.Data("KeyringData"); ok {
		kr := d.(*data.KeyringData)
		o := kr.Command(atom, data.KeyringCommandIssueToken, data.KeyringIssueTokenArgs{Subject: "fe-kr-revoke", TTL: time.Hour})
		if o.Err != nil {
			report("Keyring/revoke-issue", fmt.Sprintf("%v", o.Err), o.Err)
		} else if _, ok := o.Value.(data.KeyringToken); ok {
			report("Keyring/revoke-issue", "fe-kr-revoke", nil)
			o = kr.Command(atom, data.KeyringCommandRevokeToken, data.KeyringRevokeTokenArgs{Subject: "fe-kr-revoke"})
			report("Keyring/revoke_token", "fe-kr-revoke", o.Err)
			// 吊销后 ActiveToken 必须失效(吊销语义在令牌查找路径:
			// Verify 是低层 HMAC 原语, 按文档不查吊销表)。
			if _, active := kr.ActiveToken("fe-kr-revoke"); active {
				report("Keyring/revoked-inactive", "吊销后仍有效", fmt.Errorf("revoked token still active"))
			} else {
				report("Keyring/revoked-inactive", "吊销后不再有效", nil)
			}
			// 重复吊销 → 拒绝
			o = kr.Command(atom, data.KeyringCommandRevokeToken, data.KeyringRevokeTokenArgs{Subject: "fe-kr-revoke"})
			if o.Err == nil {
				report("Keyring/revoke-twice", "应拒绝但成功", fmt.Errorf("double revoke accepted"))
			} else {
				report("Keyring/revoke-twice", "正确拒绝", nil)
			}
		} else if o.Err != nil {
			report("Keyring/revoke-issue", fmt.Sprintf("%v", o.Value), o.Err)
		} else {
			report("Keyring/revoke-issue", fmt.Sprintf("%T", o.Value), fmt.Errorf("issue returned %T (want data.KeyringToken)", o.Value))
		}
	}

	// --- CloudRoleAbility: describe/get_status 值段 + unregister 往返 ---
	if a, ok := extAtom.Ability("CloudRoleAbility"); ok {
		o := a.Command(extAtom, ability.CloudRoleCommandDescribe, nil)
		report("Cloud/describe", fmt.Sprintf("%v", o.Value), o.Err)
		o = a.Command(extAtom, ability.CloudRoleCommandGetStatus, nil)
		report("Cloud/get_status", fmt.Sprintf("%v", o.Value), o.Err)
		// 现有段已 register svc-1 → unregister 后 list 断言移除
		o = a.Command(extAtom, ability.CloudRoleCommandUnregister, ability.CloudRoleUnregisterServiceArgs{Name: "svc-1"})
		if o.Err != nil {
			report("Cloud/unregister_service", fmt.Sprintf("%v", o.Err), o.Err)
		} else {
			report("Cloud/unregister_service", "svc-1", nil)
			o = a.Command(extAtom, ability.CloudRoleCommandListServices, nil)
			if svcs, ok := o.Value.([]ability.CloudRoleService); ok {
				gone := true
				for _, s := range svcs {
					if s.Name == "svc-1" {
						gone = false
						break
					}
				}
				if !gone {
					report("Cloud/unregister-reflected", fmt.Sprintf("services=%d", len(svcs)), fmt.Errorf("svc-1 still listed after unregister"))
				} else {
					report("Cloud/unregister-reflected", fmt.Sprintf("services=%d", len(svcs)), nil)
				}
			} else if o.Err != nil {
				report("Cloud/unregister-reflected", fmt.Sprintf("%v", o.Value), o.Err)
			} else {
				report("Cloud/unregister-reflected", fmt.Sprintf("%T", o.Value), fmt.Errorf("list_services returned %T (want []ability.CloudRoleService)", o.Value))
			}
		}
	}

	// --- EdgeRoleAbility: set_capabilities 整表替换 + remove_capability 盲区 ---
	// 注意: main 的 CloudRole 段已把 role 切到 "cloud", 这里必须先切回 "edge",
	// 否则 EdgeRole 的 role 依赖(edge)全部拒绝——非缺陷, 是 verify 段顺序约束。
	if a, ok := extAtom.Ability("EdgeRoleAbility"); ok {
		if ra, ok2 := extAtom.Ability("RoleAbility"); ok2 {
			_ = ra.Command(extAtom, ability.CommandSetRole, ability.RoleAbilityArgs{Role: "edge"})
		}
		o := a.Command(extAtom, ability.EdgeRoleCommandSetCaps, ability.EdgeRoleSetCapabilitiesArgs{Capabilities: []string{"opcua", "serial"}})
		report("EdgeRole/set_capabilities", "opcua,serial", o.Err)
		o = a.Command(extAtom, ability.EdgeRoleCommandListCaps, nil)
		if caps, ok := o.Value.([]string); ok {
			if len(caps) != 2 || caps[0] != "opcua" || caps[1] != "serial" {
				report("EdgeRole/set_capabilities-reflected", fmt.Sprintf("%v", caps), fmt.Errorf("set_capabilities not reflected: %v", caps))
			} else {
				report("EdgeRole/set_capabilities-reflected", fmt.Sprintf("%v", caps), nil)
			}
		} else if o.Err != nil {
			report("EdgeRole/set_capabilities-reflected", fmt.Sprintf("%v", o.Value), o.Err)
		} else {
			report("EdgeRole/set_capabilities-reflected", fmt.Sprintf("%T", o.Value), fmt.Errorf("list_capabilities returned %T (want []string)", o.Value))
		}
		o = a.Command(extAtom, ability.EdgeRoleCommandRemoveCap, ability.EdgeRoleCapabilityArg{Name: "opcua"})
		report("EdgeRole/remove_capability", "opcua", o.Err)
		o = a.Command(extAtom, ability.EdgeRoleCommandListCaps, nil)
		if caps, ok := o.Value.([]string); ok {
			gone := true
			for _, c := range caps {
				if c == "opcua" {
					gone = false
					break
				}
			}
			if !gone {
				report("EdgeRole/remove_capability-reflected", fmt.Sprintf("%v", caps), fmt.Errorf("opcua still listed after remove"))
			} else {
				report("EdgeRole/remove_capability-reflected", fmt.Sprintf("%v", caps), nil)
			}
		} else if o.Err != nil {
			report("EdgeRole/remove_capability-reflected", fmt.Sprintf("%v", o.Value), o.Err)
		} else {
			report("EdgeRole/remove_capability-reflected", fmt.Sprintf("%T", o.Value), fmt.Errorf("list_capabilities returned %T (want []string)", o.Value))
		}
	}
}

var _ = types.CommandOutput{}

// mapBashToSh 把 BashAbility 的管理命令常量映射到对应的 Sh 常量名(仅作
// verify 段选择命令的辅助——Bash 的 Command 只认 BashCommand* 常量)。
func mapBashToSh(abilityName, shCmd string) string {
	if abilityName != "BashAbility" {
		return shCmd
	}
	switch shCmd {
	case ability.ShCommandKill:
		return ability.BashCommandKill
	case ability.ShCommandWait:
		return ability.BashCommandWait
	case ability.ShCommandList:
		return ability.BashCommandList
	case ability.ShCommandStart:
		return ability.BashCommandStart
	case ability.ShCommandSetAllowlist:
		return ability.BashCommandSetAllowlist
	}
	return shCmd
}
