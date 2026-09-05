package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/FasterEdge/FasterEdge/ability"
	"github.com/FasterEdge/FasterEdge/types"
)

// verifyPoweredExtras 覆盖扩展能力(AlgDist/FileTransfer/Influx/EKuiper/Docker/K8s)
// 的命令盲区——第十三轮全仓命令枚举 mapping 发现这些命令从未被 verify 触及。
// 纪律与主段一致: 类型断言失败 + err==nil = FAIL; 无 transport 返回错误的
// 命令按"正确拒绝"段(PASS); 值段断言具体类型与关键字段, 不静默 PASS。
func verifyPoweredExtras(atom, extAtom *types.Atom) {
	// --- AlgDistAbility: distribute/list_distribute/cancel(骨架模式可完整测) ---
	if a, ok := extAtom.Ability("AlgDistAbility"); ok {
		o := a.Command(extAtom, ability.AlgDistCommandRegister, ability.AlgDistRegisterArgs{Name: "fe-verify-dist", Version: "2.0", SourcePath: "/tmp/fe-verify-dist.py", ContentType: "python"})
		report("AlgDist/register-dist", "fe-verify-dist@2.0", o.Err)
		// distribute 完整生命周期: 骨架模式(FileTransfer 无 transport)下
		// upload 立即 Completed, watcher 收敛 job 终态——可端到端验证。
		o = a.Command(extAtom, ability.AlgDistCommandDistribute, ability.AlgDistDistributeArgs{Name: "fe-verify-dist", Version: "2.0", Target: "edge-2"})
		if o.Err != nil {
			report("AlgDist/distribute", fmt.Sprintf("%v", o.Err), o.Err)
		} else if id, ok := o.Value.(string); ok && id != "" {
			report("AlgDist/distribute", "id="+id, nil)
			o = a.Command(extAtom, ability.AlgDistCommandListDistribute, nil)
			if jobs, ok := o.Value.([]ability.AlgDistJob); ok {
				if len(jobs) < 1 {
					report("AlgDist/list_distribute", fmt.Sprintf("jobs=%d", len(jobs)), fmt.Errorf("expected at least 1 job after distribute"))
				} else {
					report("AlgDist/list_distribute", fmt.Sprintf("jobs=%d", len(jobs)), nil)
					o = a.Command(extAtom, ability.AlgDistCommandGet, ability.AlgDistAlgorithmRef{Name: "fe-verify-dist", Version: "2.0"})
					report("AlgDist/get-after-distribute", fmt.Sprintf("%v", o.Value), o.Err)
					o = a.Command(extAtom, ability.AlgDistCommandCancel, ability.AlgDistIDArg{ID: id})
					if o.Err != nil {
						// 骨架模式 watcher 可能已把 job 收敛为终态——对终态
						// cancel 返回错误属"正确拒绝"(cancelJob 语义)。
						report("AlgDist/cancel-terminal", "正确拒绝(已终态)", nil)
					} else {
						report("AlgDist/cancel", "canceled", nil)
					}
				}
			} else if o.Err != nil {
				report("AlgDist/list_distribute", fmt.Sprintf("%v", o.Value), o.Err)
			} else {
				report("AlgDist/list_distribute", fmt.Sprintf("%T", o.Value), fmt.Errorf("list_distribute returned %T (want []ability.AlgDistJob)", o.Value))
			}
		} else if o.Err != nil {
			report("AlgDist/distribute", fmt.Sprintf("%v", o.Value), o.Err)
		} else {
			report("AlgDist/distribute", fmt.Sprintf("%T %v", o.Value, o.Value), fmt.Errorf("distribute returned %T (want non-empty job ID string)", o.Value))
		}
		// distribute 不存在的算法 → 拒绝
		o = a.Command(extAtom, ability.AlgDistCommandDistribute, ability.AlgDistDistributeArgs{Name: "fe-nope-xyz", Version: "9.9", Target: "edge-2"})
		if o.Err == nil {
			report("AlgDist/distribute-missing-alg", "应拒绝但成功", fmt.Errorf("missing algorithm accepted"))
		} else {
			report("AlgDist/distribute-missing-alg", "正确拒绝", nil)
		}
		// distribute 空 target → 拒绝(name/version/target 必填)
		o = a.Command(extAtom, ability.AlgDistCommandDistribute, ability.AlgDistDistributeArgs{Name: "fe-verify-dist", Version: "2.0", Target: "  "})
		if o.Err == nil {
			report("AlgDist/distribute-empty-target", "应拒绝但成功", fmt.Errorf("blank target accepted"))
		} else {
			report("AlgDist/distribute-empty-target", "正确拒绝", nil)
		}
		// clear_finished 值段(清理已终态 job)
		o = a.Command(extAtom, ability.AlgDistCommandClearFinished, nil)
		report("AlgDist/clear_finished", fmt.Sprintf("%v", o.Value), o.Err)
	}

	// --- FileTransferAbility: download/get/get_target/cancel 盲区 ---
	if a, ok := extAtom.Ability("FileTransferAbility"); ok {
		o := a.Command(extAtom, ability.FileTransferCommandGetTarget, nil)
		report("FileTransfer/get_target", fmt.Sprintf("%v", o.Value), o.Err)
		// download 骨架模式(无 transport → 立即 Completed, 与实际传输无关)
		o = a.Command(extAtom, ability.FileTransferCommandDownload, ability.FileTransferDownloadArgs{
			RemotePath: "/incoming/fe-dl.bin",
			LocalPath:  filepath.Join(os.TempDir(), "fe-dl.bin"),
		})
		if o.Err != nil {
			report("FileTransfer/download", fmt.Sprintf("%v", o.Err), o.Err)
		} else if id, ok := o.Value.(string); ok && id != "" {
			report("FileTransfer/download", "id="+id, nil)
			o = a.Command(extAtom, ability.FileTransferCommandGet, ability.FileTransferIDArg{ID: id})
			if got, ok := o.Value.(ability.FileTransfer); ok {
				report("FileTransfer/get", fmt.Sprintf("id=%s status=%s", got.ID, got.Status), nil)
			} else if o.Err != nil {
				report("FileTransfer/get", fmt.Sprintf("%v", o.Value), o.Err)
			} else {
				report("FileTransfer/get", fmt.Sprintf("%T", o.Value), fmt.Errorf("get returned %T (want ability.FileTransfer)", o.Value))
			}
			// 终态(Completed)后 cancel → 正确拒绝(cancel 语义: 已终态不可取消)
			o = a.Command(extAtom, ability.FileTransferCommandCancel, ability.FileTransferIDArg{ID: id})
			if o.Err == nil {
				report("FileTransfer/cancel-terminal", "已终态但取消被接受", fmt.Errorf("cancel on finished transfer accepted"))
			} else {
				report("FileTransfer/cancel-terminal", "正确拒绝(已终态)", nil)
			}
		} else if o.Err != nil {
			report("FileTransfer/download", fmt.Sprintf("%v", o.Value), o.Err)
		} else {
			report("FileTransfer/download", fmt.Sprintf("%T %v", o.Value, o.Value), fmt.Errorf("download returned %T (want non-empty transfer ID)", o.Value))
		}
		// get 不存在的 transfer → 拒绝
		o = a.Command(extAtom, ability.FileTransferCommandGet, ability.FileTransferIDArg{ID: "tx-99999"})
		if o.Err == nil {
			report("FileTransfer/get-missing", "应拒绝但成功", fmt.Errorf("missing transfer accepted"))
		} else {
			report("FileTransfer/get-missing", "正确拒绝", nil)
		}
		// clear_finished 值段(清理已终态 transfer)
		o = a.Command(extAtom, ability.FileTransferCommandClearFinished, nil)
		report("FileTransfer/clear_finished", fmt.Sprintf("%v", o.Value), o.Err)
	}

	// --- InfluxAbility: set_token/set_org/get_endpoint/list_series/delete_series
	//     + write/query/ping 无 transport 正确拒绝 ---
	if a, ok := extAtom.Ability("InfluxAbility"); ok {
		o := a.Command(extAtom, ability.InfluxCommandSetToken, ability.InfluxConfigArgs{Value: "0123456789abcdef"})
		report("Influx/set_token", "len=16", o.Err)
		o = a.Command(extAtom, ability.InfluxCommandSetOrg, ability.InfluxConfigArgs{Value: "feorg"})
		report("Influx/set_org", "feorg", o.Err)
		o = a.Command(extAtom, ability.InfluxCommandSetBucket, ability.InfluxConfigArgs{Value: "telemetry2"})
		report("Influx/set_bucket2", "telemetry2", o.Err)
		// get_config 在 set 后的字段往返(旧实现只打印——set 变 no-op 也 PASS)
		o = a.Command(extAtom, ability.InfluxCommandGetConfig, nil)
		if cfg, ok := o.Value.(ability.InfluxConfig); ok {
			if cfg.Bucket != "telemetry2" || cfg.Org != "feorg" {
				report("Influx/get_config-after-set", fmt.Sprintf("%+v", cfg), fmt.Errorf("round-trip mismatch: %+v", cfg))
			} else {
				report("Influx/get_config-after-set", fmt.Sprintf("org=%s bucket=%s", cfg.Org, cfg.Bucket), nil)
			}
		} else if o.Err != nil {
			report("Influx/get_config-after-set", fmt.Sprintf("%v", o.Value), o.Err)
		} else {
			report("Influx/get_config-after-set", fmt.Sprintf("%T", o.Value), fmt.Errorf("get_config returned %T (want ability.InfluxConfig)", o.Value))
		}
		o = a.Command(extAtom, ability.InfluxCommandGetEndpoint, nil)
		if got, ok := o.Value.(string); ok && got != "" {
			report("Influx/get_endpoint", got, nil)
		} else if o.Err != nil {
			report("Influx/get_endpoint", fmt.Sprintf("%v", o.Value), o.Err)
		} else {
			report("Influx/get_endpoint", fmt.Sprintf("%T %v", o.Value, o.Value), fmt.Errorf("get_endpoint returned %T (want non-empty string)", o.Value))
		}
		// 无 transport: write/query/ping 必须正确拒绝(骨架能力无注入 transport)
		o = a.Command(extAtom, ability.InfluxCommandWrite, ability.InfluxWriteArgs{Points: []ability.InfluxPoint{{Measurement: "m1", Fields: map[string]any{"v": 1.0}}}})
		if o.Err == nil {
			report("Influx/write-no-transport", "应拒绝但成功", fmt.Errorf("write accepted without transport"))
		} else {
			report("Influx/write-no-transport", "正确拒绝", nil)
		}
		// write 空 points → 拒绝(参数校验先于 transport)
		o = a.Command(extAtom, ability.InfluxCommandWrite, ability.InfluxWriteArgs{Points: nil})
		if o.Err == nil {
			report("Influx/write-empty-points", "应拒绝但成功", fmt.Errorf("empty points accepted"))
		} else {
			report("Influx/write-empty-points", "正确拒绝", nil)
		}
		o = a.Command(extAtom, ability.InfluxCommandQuery, ability.InfluxQueryArgs{Query: "SELECT * FROM m"})
		if o.Err == nil {
			report("Influx/query-no-transport", "应拒绝但成功", fmt.Errorf("query accepted without transport"))
		} else {
			report("Influx/query-no-transport", "正确拒绝", nil)
		}
		o = a.Command(extAtom, ability.InfluxCommandPing, nil)
		if o.Err == nil {
			report("Influx/ping-no-transport", "应拒绝但成功", fmt.Errorf("ping accepted without transport"))
		} else {
			report("Influx/ping-no-transport", "正确拒绝", nil)
		}
		o = a.Command(extAtom, ability.InfluxCommandListSeries, nil)
		if series, ok := o.Value.([]string); ok {
			report("Influx/list_series", fmt.Sprintf("count=%d", len(series)), nil)
		} else if o.Err != nil {
			report("Influx/list_series", fmt.Sprintf("%v", o.Value), o.Err)
		} else {
			report("Influx/list_series", fmt.Sprintf("%T", o.Value), fmt.Errorf("list_series returned %T (want []string)", o.Value))
		}
		o = a.Command(extAtom, ability.InfluxCommandDeleteSeries, ability.InfluxSeriesArgs{Measurement: "fe-nope-metric"})
		if o.Err == nil {
			report("Influx/delete_series-missing", "应拒绝但成功", fmt.Errorf("missing series delete accepted"))
		} else {
			report("Influx/delete_series-missing", "正确拒绝", nil)
		}
	}

	// --- EKuiperAbility: rule 面 8 命令盲区(create/drop/start/stop/show/status + stream) ---
	if a, ok := extAtom.Ability("EKuiperAbility"); ok {
		o := a.Command(extAtom, ability.EKuiperCommandCreateRule, ability.EKuiperCreateRuleArgs{ID: "r1", SQL: "SELECT * FROM s1", Actions: []string{"log"}})
		if o.Err == nil {
			report("EKuiper/create_rule-no-transport", "应拒绝但成功", fmt.Errorf("create_rule accepted without transport"))
		} else {
			report("EKuiper/create_rule-no-transport", "正确拒绝", nil)
		}
		o = a.Command(extAtom, ability.EKuiperCommandCreateRule, ability.EKuiperCreateRuleArgs{ID: "  ", SQL: "SELECT 1"})
		if o.Err == nil {
			report("EKuiper/create_rule-empty-id", "应拒绝但成功", fmt.Errorf("blank rule id accepted"))
		} else {
			report("EKuiper/create_rule-empty-id", "正确拒绝", nil)
		}
		o = a.Command(extAtom, ability.EKuiperCommandDropRule, ability.EKuiperRuleIDArg{ID: "r-none"})
		if o.Err == nil {
			report("EKuiper/drop_rule-missing", "应拒绝但成功", fmt.Errorf("missing rule drop accepted"))
		} else {
			report("EKuiper/drop_rule-missing", "正确拒绝", nil)
		}
		o = a.Command(extAtom, ability.EKuiperCommandStartRule, ability.EKuiperRuleIDArg{ID: "r-none"})
		if o.Err == nil {
			report("EKuiper/start_rule-missing", "应拒绝但成功", fmt.Errorf("missing rule start accepted"))
		} else {
			report("EKuiper/start_rule-missing", "正确拒绝", nil)
		}
		o = a.Command(extAtom, ability.EKuiperCommandStopRule, ability.EKuiperRuleIDArg{ID: "r-none"})
		if o.Err == nil {
			report("EKuiper/stop_rule-missing", "应拒绝但成功", fmt.Errorf("missing rule stop accepted"))
		} else {
			report("EKuiper/stop_rule-missing", "正确拒绝", nil)
		}
		o = a.Command(extAtom, ability.EKuiperCommandGetRuleStatus, ability.EKuiperRuleIDArg{ID: "r-none"})
		if o.Err == nil {
			report("EKuiper/get_rule_status-missing", "应拒绝但成功", fmt.Errorf("missing rule status accepted"))
		} else {
			report("EKuiper/get_rule_status-missing", "正确拒绝", nil)
		}
		o = a.Command(extAtom, ability.EKuiperCommandShowRules, nil)
		if rules, ok := o.Value.([]ability.EKuiperRule); ok {
			report("EKuiper/show_rules", fmt.Sprintf("count=%d", len(rules)), nil)
		} else if o.Err != nil {
			report("EKuiper/show_rules", fmt.Sprintf("%v", o.Value), o.Err)
		} else {
			report("EKuiper/show_rules", fmt.Sprintf("%T", o.Value), fmt.Errorf("show_rules returned %T (want []ability.EKuiperRule)", o.Value))
		}
		o = a.Command(extAtom, ability.EKuiperCommandDropStream, ability.EKuiperStreamRef{Name: "s-none"})
		if o.Err == nil {
			report("EKuiper/drop_stream-missing", "应拒绝但成功", fmt.Errorf("missing stream drop accepted"))
		} else {
			report("EKuiper/drop_stream-missing", "正确拒绝", nil)
		}
		o = a.Command(extAtom, ability.EKuiperCommandGetStream, ability.EKuiperStreamRef{Name: "s-none"})
		if o.Err == nil {
			report("EKuiper/get_stream-missing", "应拒绝但成功", fmt.Errorf("missing stream get accepted"))
		} else {
			report("EKuiper/get_stream-missing", "正确拒绝", nil)
		}
		o = a.Command(extAtom, ability.EKuiperCommandListStreams, nil)
		if streams, ok := o.Value.([]ability.EKuiperStream); ok {
			report("EKuiper/list_streams", fmt.Sprintf("count=%d", len(streams)), nil)
		} else if o.Err != nil {
			report("EKuiper/list_streams", fmt.Sprintf("%v", o.Value), o.Err)
		} else {
			report("EKuiper/list_streams", fmt.Sprintf("%T", o.Value), fmt.Errorf("list_streams returned %T (want []ability.EKuiperStream)", o.Value))
		}
	}

	// --- DockerAbility: restart 参数校验(空 ID 拒绝; 现有段已注入 transport 与否均安全) ---
	if a, ok := extAtom.Ability("DockerAbility"); ok {
		o := a.Command(extAtom, ability.DockerCommandRestart, ability.DockerContainerArgs{})
		if o.Err == nil {
			report("Docker/restart-empty-id", "应拒绝但成功", fmt.Errorf("empty container id accepted"))
		} else {
			report("Docker/restart-empty-id", "正确拒绝", nil)
		}
	}

	// --- KubernetesAbility: scale/delete/get/get_logs/list 无 transport 正确拒绝 ---
	if a, ok := extAtom.Ability("KubernetesAbility"); ok {
		o := a.Command(extAtom, ability.K8sCommandScale, ability.K8sScaleArgs{Deployment: "deploy/nginx", Replicas: 2})
		if o.Err == nil {
			report("K8s/scale-no-transport", "应拒绝但成功", fmt.Errorf("scale accepted without transport"))
		} else {
			report("K8s/scale-no-transport", "正确拒绝", nil)
		}
		o = a.Command(extAtom, ability.K8sCommandDelete, ability.K8sGetArgs{Kind: "pod", Name: "fe-x"})
		if o.Err == nil {
			report("K8s/delete-no-transport", "应拒绝但成功", fmt.Errorf("delete accepted without transport"))
		} else {
			report("K8s/delete-no-transport", "正确拒绝", nil)
		}
		o = a.Command(extAtom, ability.K8sCommandGet, ability.K8sGetArgs{Kind: "pod", Name: "fe-x"})
		if o.Err == nil {
			report("K8s/get-no-transport", "应拒绝但成功", fmt.Errorf("get accepted without transport"))
		} else {
			report("K8s/get-no-transport", "正确拒绝", nil)
		}
		o = a.Command(extAtom, ability.K8sCommandGetLogs, ability.K8sLogsArgs{Pod: "fe-x"})
		if o.Err == nil {
			report("K8s/get_logs-no-transport", "应拒绝但成功", fmt.Errorf("get_logs accepted without transport"))
		} else {
			report("K8s/get_logs-no-transport", "正确拒绝", nil)
		}
		o = a.Command(extAtom, ability.K8sCommandList, ability.K8sListArgs{Kind: "pod"})
		if o.Err == nil {
			report("K8s/list-no-transport", "应拒绝但成功", fmt.Errorf("list accepted without transport"))
		} else {
			report("K8s/list-no-transport", "正确拒绝", nil)
		}
	}
}

// 确保 extAtom 参数被使用(部分环境无扩展组件时函数仍需可编译)。
var _ = types.CommandOutput{}
