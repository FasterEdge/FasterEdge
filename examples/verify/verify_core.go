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
	}

	// --- ShAbility/BashAbility: get_allowlist 往返(值段, 返回 ShAllowlist 快照) ---
	for _, pair := range []struct{ name, getCmd string }{
		{"ShAbility", ability.ShCommandGetAllowlist},
		{"BashAbility", ability.BashCommandGetAllowlist},
	} {
		if a, ok := extAtom.Ability(pair.name); ok {
			o := a.Command(extAtom, pair.getCmd, nil)
			if al, ok := o.Value.(ability.ShAllowlist); ok {
				report(pair.name+"/get_allowlist", fmt.Sprintf("shell=%s entries=%d", al.Shell, len(al.Allowed)), nil)
			} else if o.Err != nil {
				report(pair.name+"/get_allowlist", fmt.Sprintf("%v", o.Value), o.Err)
			} else {
				report(pair.name+"/get_allowlist", fmt.Sprintf("%T", o.Value), fmt.Errorf("get_allowlist returned %T (want ability.ShAllowlist)", o.Value))
			}
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
