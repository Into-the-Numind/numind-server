import fitz  # PyMuPDF
import sys
import json
import os
from docx import Document

def extract_pdf_content(pdf_path):
    try:
        doc = fitz.open(pdf_path)
        full_text = []
        for page_index in range(len(doc)):
            page = doc[page_index]
            blocks = page.get_text("blocks")
            page_content = []
            for b in blocks:
                if b[6] == 0:  # Text block
                    text = b[4].strip()
                    if text:
                        clean_text = text.replace("\n", " ")
                        page_content.append(clean_text)
            if page_content:
                full_text.append("\n\n".join(page_content))
        doc.close()
        return {"success": True, "content": "\n\n---\n\n".join(full_text)}
    except Exception as e:
        return {"success": False, "error": f"PDF extraction failed: {str(e)}"}

def extract_docx_content(docx_path):
    try:
        doc = Document(docx_path)
        full_text = []
        # 1. 提取所有段落
        for para in doc.paragraphs:
            if para.text.strip():
                full_text.append(para.text.strip())
        
        # 2. 提取表格（可选，将其转换为 Markdown 格式）
        for table in doc.tables:
            for row in table.rows:
                row_text = [cell.text.strip() for cell in row.cells]
                full_text.append(" | ".join(row_text))
                
        return {"success": True, "content": "\n\n".join(full_text)}
    except Exception as e:
        return {"success": False, "error": f"DOCX extraction failed: {str(e)}"}

def extract_txt_content(txt_path):
    try:
        with open(txt_path, 'r', encoding='utf-8', errors='ignore') as f:
            content = f.read()
        return {"success": True, "content": content}
    except Exception as e:
        return {"success": False, "error": f"TXT extraction failed: {str(e)}"}

if __name__ == "__main__":
    if len(sys.argv) < 2:
        print(json.dumps({"success": False, "error": "No file path provided"}))
        sys.exit(1)
        
    file_path = sys.argv[1]
    ext = os.path.splitext(file_path)[1].lower()
    
    if ext == ".pdf":
        result = extract_pdf_content(file_path)
    elif ext == ".docx":
        result = extract_docx_content(file_path)
    elif ext in [".txt", ".md"]:
        result = extract_txt_content(file_path)
    else:
        result = {"success": False, "error": f"Unsupported extension: {ext}"}
        
    print(json.dumps(result))
