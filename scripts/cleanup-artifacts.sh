#!/bin/bash

# Script to clean up GitHub Actions artifacts
# This script helps free up storage space by deleting old artifacts

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo -e "${YELLOW}GitHub Actions Artifact Cleanup Script${NC}"
echo "=========================================="

# Check if gh CLI is installed
if ! command -v gh &> /dev/null; then
    echo -e "${RED}Error: GitHub CLI (gh) is not installed.${NC}"
    echo "Please install it from: https://cli.github.com/"
    exit 1
fi

# Check if user is authenticated
if ! gh auth status &> /dev/null; then
    echo -e "${RED}Error: Not authenticated with GitHub CLI.${NC}"
    echo "Please run: gh auth login"
    exit 1
fi

# Get repository name
REPO=$(gh repo view --json nameWithOwner -q .nameWithOwner)
echo -e "${GREEN}Repository: ${REPO}${NC}"

# Function to list artifacts
list_artifacts() {
    echo -e "\n${YELLOW}Current artifacts:${NC}"
    gh api repos/${REPO}/actions/artifacts --paginate | jq -r '.artifacts[] | "\(.id) - \(.name) - \(.created_at) - \(.size_in_bytes) bytes"'
}

# Function to delete specific artifact
delete_artifact() {
    local artifact_id=$1
    echo -e "\n${YELLOW}Deleting artifact ID: ${artifact_id}${NC}"
    gh api repos/${REPO}/actions/artifacts/${artifact_id} -X DELETE
    echo -e "${GREEN}✓ Artifact deleted successfully${NC}"
}

# Function to delete artifacts older than X days
delete_old_artifacts() {
    local days=$1
    echo -e "\n${YELLOW}Deleting artifacts older than ${days} days...${NC}"
    
    # Get current timestamp
    local current_time=$(date +%s)
    local cutoff_time=$((current_time - days * 24 * 60 * 60))
    
    # Get artifacts and filter by date
    gh api repos/${REPO}/actions/artifacts --paginate | \
    jq -r --arg cutoff "$cutoff_time" '
        .artifacts[] | 
        select(.created_at | fromdateiso8601 < ($cutoff | tonumber)) | 
        .id
    ' | while read artifact_id; do
        if [ ! -z "$artifact_id" ]; then
            echo "Deleting artifact ID: $artifact_id"
            gh api repos/${REPO}/actions/artifacts/${artifact_id} -X DELETE
        fi
    done
    
    echo -e "${GREEN}✓ Old artifacts deleted successfully${NC}"
}

# Function to delete all artifacts
delete_all_artifacts() {
    echo -e "\n${RED}WARNING: This will delete ALL artifacts!${NC}"
    read -p "Are you sure you want to continue? (y/N): " -n 1 -r
    echo
    
    if [[ $REPLY =~ ^[Yy]$ ]]; then
        echo -e "${YELLOW}Deleting all artifacts...${NC}"
        
        gh api repos/${REPO}/actions/artifacts --paginate | \
        jq -r '.artifacts[].id' | while read artifact_id; do
            if [ ! -z "$artifact_id" ]; then
                echo "Deleting artifact ID: $artifact_id"
                gh api repos/${REPO}/actions/artifacts/${artifact_id} -X DELETE
            fi
        done
        
        echo -e "${GREEN}✓ All artifacts deleted successfully${NC}"
    else
        echo -e "${YELLOW}Operation cancelled${NC}"
    fi
}

# Function to show storage usage
show_storage_usage() {
    echo -e "\n${YELLOW}Storage usage:${NC}"
    gh api repos/${REPO}/actions/artifacts --paginate | \
    jq -r '.artifacts | map(.size_in_bytes) | add' | \
    awk '{printf "Total size: %.2f MB\n", $1/1024/1024}'
}

# Main menu
while true; do
    echo -e "\n${YELLOW}Choose an option:${NC}"
    echo "1) List all artifacts"
    echo "2) Show storage usage"
    echo "3) Delete artifacts older than 7 days"
    echo "4) Delete artifacts older than 30 days"
    echo "5) Delete specific artifact (by ID)"
    echo "6) Delete ALL artifacts (DANGEROUS)"
    echo "7) Exit"
    
    read -p "Enter your choice (1-7): " choice
    
    case $choice in
        1)
            list_artifacts
            ;;
        2)
            show_storage_usage
            ;;
        3)
            delete_old_artifacts 7
            ;;
        4)
            delete_old_artifacts 30
            ;;
        5)
            read -p "Enter artifact ID: " artifact_id
            if [ ! -z "$artifact_id" ]; then
                delete_artifact $artifact_id
            fi
            ;;
        6)
            delete_all_artifacts
            ;;
        7)
            echo -e "${GREEN}Goodbye!${NC}"
            exit 0
            ;;
        *)
            echo -e "${RED}Invalid choice. Please try again.${NC}"
            ;;
    esac
done 