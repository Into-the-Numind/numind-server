package util

import (
	"encoding/json"
	"reflect"
)

// FilterFields 根据指定字段过滤结构体，只返回需要的字段
func FilterFields(data interface{}, fields []string) interface{} {
	if len(fields) == 0 {
		return data
	}

	// 将数据转换为JSON，然后解析为map
	dataBytes, err := json.Marshal(data)
	if err != nil {
		return data
	}

	var dataMap map[string]interface{}
	if err := json.Unmarshal(dataBytes, &dataMap); err != nil {
		return data
	}

	// 创建新的map，只包含指定字段
	filteredMap := make(map[string]interface{})
	for _, field := range fields {
		if value, exists := dataMap[field]; exists {
			filteredMap[field] = value
		}
	}

	return filteredMap
}

// FilterSliceFields 过滤切片中的每个元素
func FilterSliceFields(data interface{}, fields []string) interface{} {
	if reflect.TypeOf(data).Kind() != reflect.Slice {
		return data
	}

	// 将切片转换为JSON，然后解析
	dataBytes, err := json.Marshal(data)
	if err != nil {
		return data
	}

	var sliceData []interface{}
	if err := json.Unmarshal(dataBytes, &sliceData); err != nil {
		return data
	}

	// 过滤每个元素
	filteredSlice := make([]interface{}, 0, len(sliceData))
	for _, item := range sliceData {
		if itemMap, ok := item.(map[string]interface{}); ok {
			filteredItem := make(map[string]interface{})
			for _, field := range fields {
				if value, exists := itemMap[field]; exists {
					filteredItem[field] = value
				}
			}
			filteredSlice = append(filteredSlice, filteredItem)
		}
	}

	return filteredSlice
}

// CreateMinimalResponse 创建最小化响应，只包含必要字段
func CreateMinimalResponse(data interface{}, fields []string) map[string]interface{} {
	if len(fields) == 0 {
		return map[string]interface{}{
			"data": data,
		}
	}

	filteredData := FilterFields(data, fields)
	return map[string]interface{}{
		"data": filteredData,
	}
}
