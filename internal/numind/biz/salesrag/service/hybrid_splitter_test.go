package service

import (
	"fmt"
	"testing"
)

func TestHybridSplitter_SemanticSplit(t *testing.T) {
	// 1. 创建混合切分器
	h := NewDefaultHybridSplitter()

	// 2. 检查语义服务是否可用
	if !h.IsSemanticAvailable() {
		t.Log("Semantic server not detected, skipping semantic test part (but checking fallback)")
	}

	// 3. 构造一段长文本（超过 500 字符，触发语义切分）
	text := `人工智能（AI）是模拟、延伸和扩展人的智能理论、方法、技术及应用系统的一门新的技术科学。
人工智能是计算机科学的一个分支，它企图了解智能的实质，并生产出一种新的能以人类智能相似的方式做出反应的智能机器。
该领域的研究包括机器人、语言识别、图像识别、自然语言处理和专家系统等。
自人工智能诞生以来，理论和技术日益成熟，应用领域也不断扩大，可以设想，未来人工智能带来的科技产品，将会是人类智慧的“容器”。
人工智能可以对人的意识、思维的信息过程的模拟。人工智能不是人的智能，但能像人那样思考、也可能超过人的智能。

这里是第二部分的内容，人工智能在医疗领域的应用非常广泛。
通过分析大量的医疗数据，AI可以帮助医生更准确地诊断疾病。
例如，在医学影像分析中，AI系统可以自动识别X射线、CT扫描或MRI图像中的异常情况，如肿瘤或骨折。
这不仅提高了诊断的速度，还减少了人为错误的风险。
此外，AI还可以支持个性化治疗方案的制定，根据患者的基因组成和病史推荐最合适的药物和剂量。`

	// 确保文本长度超过 500
	for i := 0; i < 2; i++ {
		text += text
	}
	fmt.Printf("Input text length: %d\n", len(text))

	// 4. 执行切分
	chunks, details, err := h.SplitWithDetails(text)
	if err != nil {
		t.Fatalf("Split failed: %v", err)
	}

	fmt.Printf("Strategy used: %v\n", details["strategy"])
	fmt.Printf("Generated %d chunks\n", len(chunks))

	if len(chunks) == 0 {
		t.Fatal("No chunks generated")
	}

	// 验证每个 chunk 的长度
	maxSize := h.cfg.SemanticConfig.MaxChunkSize
	for i, chunk := range chunks {
		fmt.Printf("Chunk %d length: %d\n", i, len(chunk.Content))
		// 注意：如果有 overlap，长度可能会稍微超过 MaxChunkSize
		// 但语义切分服务的 max_chunk_size 是硬限制
		if len(chunk.Content) > maxSize+500 { // 宽松点检查，因为有上下文衔接
			t.Errorf("Chunk %d is too large: %d", i, len(chunk.Content))
		}
	}
}
