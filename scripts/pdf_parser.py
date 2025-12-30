import fitz  # PyMuPDF
import sys
import json
import os

def extract_pdf_content(pdf_path):
    try:
        if not os.path.exists(pdf_path):
            return {"error": f"File not found: {pdf_path}"}

        doc = fitz.open(pdf_path)
        full_text = []
        
        for page_index in range(len(doc)):
            page = doc[page_index]
            # 使用 "blocks" 模式获取结构化文本
            # blocks: (x0, y0, x1, y1, "text", block_no, block_type)
            blocks = page.get_text("blocks")
            
            page_content = []
            for b in blocks:
                # block_type 0 是文本，1 是图像
                if b[6] == 0:
                    text = b[4].strip()
                    if text:
                        # 替换换行符为空格，保持段落连续性
                        clean_text = text.replace("\n", " ")
                        page_content.append(clean_text)
            
            if page_content:
                full_text.append("\n\n".join(page_content))
        
        doc.close()
        
        # 将结果以 JSON 格式输出，方便 Go 解析
        return {
            "success": True,
            "content": "\n\n---\n\n".join(full_text), # 页码之间用分隔符
            "page_count": len(doc)
        }
    except Exception as e:
        return {"success": False, "error": str(e)}

if __name__ == "__main__":
    if len(sys.argv) < 2:
        print(json.dumps({"success": False, "error": "No PDF path provided"}))
        sys.exit(1)
        
    pdf_path = sys.argv[1]
    result = extract_pdf_content(pdf_path)
    print(json.dumps(result))
