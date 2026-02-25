package data

var dataPool = make(map[string]interface{})

// 获取数据
func GetData(name string) (interface{}, bool) {
	data, exists := dataPool[name]
	return data, exists
}

// 写入数据
func SetData(name string, data interface{}) {
	dataPool[name] = data
}

// 通过group读取数据
func GetDataByGroup(group string, name string) (interface{}, bool) {
	key := "_" + group + "_" + name
	data, exists := dataPool[key]
	return data, exists
}

// 通过group写入数据
func SetDataByGroup(group string, name string, data interface{}) {
	key := "_" + group + "_" + name
	dataPool[key] = data
}
