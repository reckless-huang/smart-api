// @Author sunwenbo
// 2024/8/5 19:45
package service

import (
	"errors"
	"fmt"
	"go-admin/app/smart/models"
	"go-admin/app/smart/service/dto"
	models2 "go-admin/common/models"
	"go-admin/common/utils"
	"go-admin/config"
	"golang.org/x/crypto/ssh"
	"gorm.io/gorm"
	"regexp"
	"time"
)

// 定义结构体，用于返回节点名称和 assignValue

/*
    -- 节点 --
	start: 开始节点
	userTask: 审批节点
	receive-task-node: 处理节点
	scriptTask: 任务节点
	end: 结束节点

    -- 网关 --
    exclusiveGateway: 排他网关
    parallelGateway: 并行网关
    inclusiveGateway: 包容网关

*/

type NodeInfo struct {
	Name        string `json:"name"`
	AssignValue int    `json:"assignValue"`
	Clazz       string `json:"clazz"`
}

// Handle OrderWorks
func (e *OrderWorksService) Handle(c *dto.OrderWorksHandleReq, handle int) error {
	var err error
	var model = models.OrderWorks{}

	tx := e.Orm.Debug().Begin()
	defer func() {
		if err != nil {
			tx.Rollback()
		} else {
			tx.Commit()
		}
	}()
	// 根据 ID 查找要更新的记录
	if err = tx.First(&model, c.GetId()).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			e.Log.Errorf("order works with ID '%v' not exists", c.GetId())
			return fmt.Errorf("order works with ID '%v' not exists", c.GetId())
		}
		e.Log.Errorf("Error querying order works with ID '%v': %s", c.GetId(), err)
		return fmt.Errorf("error querying order works with ID '%v': %s", c.GetId(), err)
	}

	// 获取绑定的流程数据
	flowData := model.BindFlowData
	// 记录操作历史当前节点
	curNode := model.CurrentNode

	// 获取所有节点
	nodes, ok := flowData.StrucTure["nodes"].([]interface{})
	if !ok {
		e.Log.Errorf("Nodes not found or not a valid structure")
		return fmt.Errorf("nodes not found or not a valid structure")
	}

	// 获取当前节点
	cutNodeId := findNodeByName(nodes, model.CurrentNode)
	if cutNodeId == "" {
		e.Log.Errorf("Current node '%v' not found in flow structure", model.CurrentNode)
		return fmt.Errorf("current node '%v' not found in flow structure", model.CurrentNode)
	}

	// 获取边缘（包含流程顺序、以及源和目标节点）
	edges, ok := flowData.StrucTure["edges"].([]interface{})
	if !ok {
		e.Log.Errorf("Edges not found or not a slice")
		return fmt.Errorf("edges not found or not a slice")
	}
	var targetNodeId string

	// 根据当前节点查找目标节点
	switch c.ActionType {
	// 1 为同意 0为拒绝
	case "1":
		targetNodeId, err = e.findNextNode(edges, cutNodeId, nodes)
		if err != nil {
			e.Log.Errorf("Error while finding next node: %v", err)
			return fmt.Errorf("error finding next node: %v", err)
		}
	case "0":
		targetNodeId = findPreviousNode(edges, cutNodeId)
	default:
		e.Log.Errorf("Invalid action type '%v'", c.ActionType)
		return fmt.Errorf("invalid action type '%v'", c.ActionType)
	}

	if targetNodeId == "" {
		targetNodeId = cutNodeId
	}

	// 获取目标节点的名称和处理人
	targetNodeInfo := findNodeInfoById(flowData.StrucTure, targetNodeId)

	if targetNodeInfo == nil {
		e.Log.Errorf("Target node information not found for ID '%v'", targetNodeId)
		return fmt.Errorf("target node information not found for ID '%v'", targetNodeId)
	}

	// 处理节点类型

	switch targetNodeInfo.Clazz {
	case "end":
		// 如果是结束节点
		model.CurrentNode = targetNodeInfo.Name
		model.CurrentHandlerID = model.CreateBy
		model.CurrentHandler = model.Creator
		model.Status = "termination"
	case "start":
		// 如果是开始节点
		model.CurrentNode = targetNodeInfo.Name
		model.CurrentHandlerID = model.CreateBy
		model.CurrentHandler = model.Creator
	default:
		// 其他节点
		model.CurrentNode = targetNodeInfo.Name
		model.CurrentHandlerID = targetNodeInfo.AssignValue
		model.CurrentHandler, err = GetHandlerNameByID(tx, targetNodeInfo.AssignValue)
		if err != nil {
			e.Log.Errorf("Error getting handler name for ID '%v': %s", targetNodeInfo.AssignValue, err)
			return fmt.Errorf("error getting handler name for ID '%v': %s", targetNodeInfo.AssignValue, err)
		}
	}

	beforeUpdate := model.UpdatedAt

	// 保存更新
	if err = tx.Save(&model).Error; err != nil {
		e.Log.Errorf("Failed to update order works: %s", err)
		return fmt.Errorf("failed to update order works: %s", err)
	}

	// 记录操作历史
	historyReq := dto.OrderWorksHistReq{
		Title:          model.Title,
		Transfer:       "工单流转", // 假设流转操作类型保存在 ActionType 中
		Remark:         "工单处理",
		CurrentNode:    curNode,
		Status:         c.ActionType,
		HandlerId:      handle,
		HandleTime:     models2.JSONTime(time.Now()),
		HandleDuration: calculateHandleDuration(beforeUpdate),
	}
	if err = RecordOperationHistory(tx, &historyReq); err != nil {
		e.Log.Errorf("Failed to record operation history: %s", err)
		return fmt.Errorf("failed to record operation history: %s", err)
	}

	return nil
}

// 辅助函数：根据节点名称查找节点 ID
func findNodeByName(nodes []interface{}, currentNode string) string {

	for _, node := range nodes {
		// 确保 node 是一个 map[string]interface{}
		n, ok := node.(map[string]interface{})
		if !ok {
			_ = fmt.Errorf(`node is not a map[string]interface{}`)
			continue
		}

		if n["label"] == currentNode {
			return n["id"].(string)
		}
	}
	return ""
}

// 辅助函数：根据当前节点查找下一个节点
func (e *OrderWorksService) findNextNode(edges []interface{}, cutNodeId string, nodes []interface{}) (string, error) {
	// 查找当前节点的类型
	currentNodeType := getNodeTypeById(nodes, cutNodeId)

	// 判断是否为处理节点
	if currentNodeType == "receive-task-node" {
		fmt.Println("当前节点是处理节点，开始执行任务...")
		// 使用新的函数获取任务名称和机器 IP
		taskName, machine, err := getTaskAndMachineById(nodes, cutNodeId)
		if err != nil {
			return "", fmt.Errorf("获取任务和机器信息失败: %v", err)
		}

		// 查询任务和机器的详细信息
		var existingTask models.OrderTask
		if err = e.Orm.Where("name = ?", taskName).First(&existingTask).Error; err != nil {
			return "", fmt.Errorf("failed to query task: %v", err)
		}

		var existingMachine models.ExecMachine
		if err = e.Orm.Where("hostname = ?", machine).First(&existingMachine).Error; err != nil {
			return "", fmt.Errorf("failed to query machine: %v", err)
		}

		// 发送任务开始消息
		startTime := time.Now().Format(time.RFC3339)
		wsMessage := utils.Message{
			Type:        "start",
			TaskID:      existingTask.ID,
			TaskName:    existingTask.Name,
			Username:    existingMachine.UserName,
			Host:        existingMachine.Ip,
			Port:        existingMachine.Port,
			Command:     existingTask.Content,
			Output:      "",
			ErrorOutput: "",
			StartTime:   startTime,
			EndTime:     "",
			Duration:    "",
		}

		utils.Manager.BroadcastMessage(existingTask.ID, wsMessage)

		// 将任务名称和机器 IP 传递给任务执行函数
		success, err := e.executeTaskOnMachine(taskName, machine)
		if err != nil {
			return "", fmt.Errorf("任务执行失败: %v", err)
		}

		// 如果任务执行成功，返回下一个节点
		if success {
			nextNodeId := findNextNodeInEdges(edges, cutNodeId)
			fmt.Printf("任务成功，跳转到下一个节点: %v\n", nextNodeId)
			return nextNodeId, nil
		}

		return "", fmt.Errorf("任务执行失败: %v", err)

		//// 如果任务执行失败，返回上一个节点
		//fmt.Println("任务失败，返回上一个节点...")
		//previousNodeId := findPreviousNode(edges, cutNodeId)
		//return previousNodeId, nil
	}

	// 判断当前节点是否为结束节点
	if currentNodeType == "end-node" {
		return cutNodeId, nil
	}

	// 如果当前节点不是处理节点，则找到下一个节点
	nextNodeId := findNextNodeInEdges(edges, cutNodeId)
	return nextNodeId, nil
}

// 辅助函数：根据当前节点查找上一个节点
func findPreviousNode(edges []interface{}, cutNodeId string) string {
	for _, edge := range edges {
		e, ok := edge.(map[string]interface{})
		if !ok {
			continue
		}

		if e["target"] == cutNodeId {
			return e["source"].(string)
		}
	}
	return cutNodeId // Return the current node ID if no previous node is found
}

// 新增函数：根据节点ID查找节点名称和 assignValue
func findNodeInfoById(nodes models.StrucTure, nodeId string) *NodeInfo {
	// 编译正则表达式
	re := regexp.MustCompile(`start|end`)

	// 如果nodeId等于 start，则将工单创建人赋值给当前处理人，同时将id赋值给assignValue
	nodeList, ok := nodes["nodes"].([]interface{})
	if !ok {
		fmt.Println("nodes is not a slice of interface{}")
		return nil
	}

	for _, node := range nodeList {
		n, ok := node.(map[string]interface{})
		if !ok {
			fmt.Println("node is not a map[string]interface{}")
			continue
		}

		if n["id"] == nodeId {
			// 提取 clazz 字段的值
			clazz, _ := n["clazz"].(string)

			// 检查 nodeId 是否包含 "start"
			if re.MatchString(nodeId) {
				return &NodeInfo{
					Name:        n["label"].(string),
					AssignValue: 0,
					Clazz:       clazz, // 使用 clazz 作为 NodeType
				}
			}

			assignValues := n["assignValue"].([]interface{})
			if !ok {
				continue
			}
			if len(assignValues) > 0 {
				assignValue, ok := assignValues[0].(float64) // 假设 assignValue 是 float64 类型
				if !ok {
					fmt.Println("assignValue[0] is not a float64")
					continue
				}
				return &NodeInfo{
					Name:        n["label"].(string),
					AssignValue: int(assignValue),
					Clazz:       clazz,
				}
			}
		}
	}
	return nil
}

// 根据节点ID查找节点类型
func getNodeTypeById(nodes []interface{}, nodeId string) string {
	for _, node := range nodes {
		// 将每个节点断言为 map[string]interface{}
		nodeMap, ok := node.(map[string]interface{})
		if !ok {
			continue // 如果断言失败，跳过这个节点
		}
		// 检查是否存在 "id" 并且是否与传入的 nodeId 匹配
		if id, ok := nodeMap["id"].(string); ok && id == nodeId {
			// 返回节点的 "type" 属性
			if nodeType, ok := nodeMap["type"].(string); ok {
				return nodeType
			}
		}
	}
	return ""
}

// 查找下一个节点
func findNextNodeInEdges(edges []interface{}, cutNodeId string) string {
	for _, edge := range edges {
		e, ok := edge.(map[string]interface{})
		if !ok {
			continue
		}
		if e["source"] == cutNodeId {
			return e["target"].(string)
		}
	}
	return ""
}

// 获取任务以及执行节点的ip
func getTaskAndMachineById(nodes []interface{}, nodeId string) (interface{}, interface{}, error) {
	var task, machine []interface{}

	for _, node := range nodes {
		// 将每个节点断言为 map[string]interface{}
		nodeMap, ok := node.(map[string]interface{})
		if !ok {
			continue // 如果断言失败，跳过这个节点
		}

		// 检查是否存在 "id" 并且是否与传入的 nodeId 匹配
		if id, ok := nodeMap["id"].(string); ok && id == nodeId {
			// 获取任务名称和机器 IP
			if taskVal, taskOk := nodeMap["task"].([]interface{}); taskOk {
				task = taskVal
			} else {
				return nil, nil, fmt.Errorf("任务信息未找到")
			}

			if machineVal, machineOk := nodeMap["machine"].([]interface{}); machineOk {
				machine = machineVal
			} else {
				return nil, nil, fmt.Errorf("机器信息未找到")
			}

			return task, machine, nil
		}
	}

	return nil, nil, fmt.Errorf("节点 ID '%v' 未找到", nodeId)
}

// 工单任务执行
func (e OrderWorksService) executeTaskOnMachine(taskName interface{}, machineName interface{}) (bool, error) {
	var err error

	tx := e.Orm.Debug().Begin()
	defer func() {
		if err != nil {
			tx.Rollback()
		} else {
			tx.Commit()
		}
	}()

	// 查询任务
	var existingTask models.OrderTask
	if err = tx.Where("name = ?", taskName).First(&existingTask).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, fmt.Errorf("task with name '%v' not found", taskName)
		}
		return false, fmt.Errorf("failed to query task: %v", err)
	}

	// 查询机器
	var existingMachine models.ExecMachine
	if err = tx.Where("hostname = ?", machineName).First(&existingMachine).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, fmt.Errorf("machine with name '%v' not found", machineName)
		}
		return false, fmt.Errorf("failed to query machine: %v", err)
	}

	// Set up SSH configuration
	sshConfig, err := e.setupSSHConfig(existingMachine)
	if err != nil {
		return false, err
	}

	host := fmt.Sprintf("%s:%d", existingMachine.Ip, existingMachine.Port)
	client, err := ssh.Dial("tcp", host, sshConfig)
	if err != nil {
		return false, fmt.Errorf("failed to connect: %v", err)
	}
	defer client.Close()

	// 创建 Connection 实例
	conn := &utils.MachineConn{}

	// 记录任务开始时间
	startTime := time.Now()

	// 执行任务
	stdout, stderr, err := conn.ExecuteCommand(client, existingTask.Content)

	// Determine end time and duration
	endTime := time.Now()
	duration := endTime.Sub(startTime).String()

	// 创建并发送 WebSocket 消息
	wsMessage := utils.Message{
		TaskID:      existingTask.ID,
		TaskName:    existingTask.Name,
		Username:    existingMachine.UserName,
		Host:        existingMachine.Ip,
		Port:        existingMachine.Port,
		Command:     existingTask.Content,
		Output:      stdout,
		ErrorOutput: stderr,
		StartTime:   startTime.Format(time.RFC3339),
		EndTime:     endTime.Format(time.RFC3339),
		Duration:    duration,
	}

	if err != nil {
		e.Log.Errorf("Command execution failed: stdout=%v, stderr=%v, error=%v", stdout, stderr, err)
		// 记录失败历史
		err = recordExecutionHistory(tx, existingTask, existingMachine, 1, stdout, stderr, startTime)
		if err != nil {
			// 更新并发送 WebSocket 消息
			wsMessage.Type = "error"
			utils.Manager.BroadcastMessage(existingTask.ID, wsMessage)
			return false, fmt.Errorf("command execution failed: %v", err)
		}
		return false, err
	}

	fmt.Printf("任务执行成功: stdout=%v, stderr=%v\n", stdout, stderr)

	// 记录成功历史
	err = recordExecutionHistory(tx, existingTask, existingMachine, 0, stdout, stderr, startTime)
	if err != nil {
		return false, err
	}
	// 更新并发送成功的 WebSocket 消息
	wsMessage.Type = "complete"
	utils.Manager.BroadcastMessage(existingTask.ID, wsMessage)
	fmt.Println("wsMessage=", wsMessage)

	return true, nil
}

// 记录任务执行记录
func recordExecutionHistory(tx *gorm.DB, task models.OrderTask, machine models.ExecMachine, status int, stdout string, stderr string, startTime time.Time) error {
	// 计算执行时长
	duration := time.Since(startTime).Seconds()

	history := models.ExecutionHistory{
		TaskID:        task.ID,
		TaskName:      task.Name,
		MachineID:     machine.ID,
		HostName:      machine.HostName,
		Ip:            machine.Ip,
		ExecutionTime: int64(duration),
		Status:        status,
		Stdout:        stdout,
		Stderr:        stderr,
		ExecutedAt:    models2.JSONTime(time.Now()),
		Creator:       "system", // Or fetch the creator from context
	}

	if err := tx.Create(&history).Error; err != nil {
		return fmt.Errorf("failed to record execution history: %v", err)
	}
	return nil
}

// setupSSHConfig sets up the SSH configuration based on the machine's authentication type.
func (e OrderWorksService) setupSSHConfig(machine models.ExecMachine) (*ssh.ClientConfig, error) {
	var sshConfig *ssh.ClientConfig
	var password string
	var err error

	// 如果为密码认证，需要解密密码
	if machine.AuthType == "1" {
		password, err = utils.Decrypt(machine.PassWord, config.ExtConfig.AesSecrets.Key)
		if err != nil {
			e.Log.Errorf("password decryption failed: %v", err)
			return nil, fmt.Errorf("password decryption failed: %v", err)
		}
		sshConfig = &ssh.ClientConfig{
			User: machine.UserName,
			Auth: []ssh.AuthMethod{
				ssh.Password(password),
			},
			HostKeyCallback: ssh.InsecureIgnoreHostKey(),
			Timeout:         5 * time.Second,
		}
		// 私钥认证方式
	} else if machine.AuthType == "2" {
		signer, err := ssh.ParsePrivateKey([]byte(machine.PrivateKey))
		if err != nil {
			return nil, fmt.Errorf("failed to parse private key: %v", err)
		}
		sshConfig = &ssh.ClientConfig{
			User: machine.UserName,
			Auth: []ssh.AuthMethod{
				ssh.PublicKeys(signer),
			},
			HostKeyCallback: ssh.InsecureIgnoreHostKey(),
			Timeout:         5 * time.Second,
		}
	} else {
		return nil, fmt.Errorf("unsupported AuthType: %v", machine.AuthType)
	}

	return sshConfig, nil
}
