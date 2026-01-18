#!/bin/bash

# Цвета для вывода
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

# Конфигурация
API_HOST="http://localhost:8080"
DB_HOST="localhost"
DB_PORT="5432"
DB_NAME="project-sem-1"
DB_USER="validator"
DB_PASSWORD="val1dat0r"

# Временные файлы для тестирования
TEST_ZIP="test_data.zip"
TEST_TAR="test_data.tar"
TEST_CSV="test_data.csv"
RESPONSE_ZIP="response.zip"

create_test_files() {
    local level=$1
    
    if [ "$level" -eq 3 ]; then
        # Создаем тестовый CSV файл с некорректными данными для сложного уровня
        echo "id,name,category,price,create_date" > $TEST_CSV
        echo "1,item1,cat1,100,2024-01-01" >> $TEST_CSV
        echo "2,item2,cat2,200,2024-01-15" >> $TEST_CSV
        echo "3,item3,cat3,invalid_price,2024-01-20" >> $TEST_CSV
        echo "4,,cat4,400,2024-01-25" >> $TEST_CSV
        echo "5,item5,,500,2024-01-30" >> $TEST_CSV
        echo "6,item6,cat6,600,invalid_date" >> $TEST_CSV
        echo "1,item1,cat1,100,2024-01-01" >> $TEST_CSV
        
        zip $TEST_ZIP $TEST_CSV
        tar -cf $TEST_TAR $TEST_CSV
    else
        # Создаем тестовый CSV файл с корректными данными для простого и продвинутого уровней
        echo "id,name,category,price,create_date" > $TEST_CSV
        echo "1,item1,cat1,100,2024-01-01" >> $TEST_CSV
        echo "2,item2,cat2,200,2024-01-15" >> $TEST_CSV
        echo "3,item3,cat3,300,2024-01-20" >> $TEST_CSV
        
        zip $TEST_ZIP $TEST_CSV
        tar -cf $TEST_TAR $TEST_CSV
    fi
}

check_api_simple() {
    create_test_files 1

    echo -e "\nПроверка API (простой уровень)"
    
    # Проверка POST /api/v0/prices
    echo "Тестирование POST /api/v0/prices"
    response=$(curl -s -F "file=@$TEST_ZIP" "${API_HOST}/api/v0/prices")
    if [[ $response == *"total_items"* && $response == *"total_categories"* && $response == *"total_price"* ]]; then
        echo -e "${GREEN}✓ POST запрос успешен${NC}"
        
    else
        echo -e "${RED}✗ POST запрос неуспешен${NC}"
        return 1
    fi
    
    # Проверка GET /api/v0/prices
    echo "Тестирование GET /api/v0/prices"
    
    # Сохраняем текущую директорию
    current_dir=$(pwd)
    
    # Создаем временную директорию и переходим в неё
    tmp_dir=$(mktemp -d)
    cd "$tmp_dir"
    
    if ! curl -s "${API_HOST}/api/v0/prices" -o "$RESPONSE_ZIP"; then
        cd "$current_dir"
        rm -rf "$tmp_dir"
        echo -e "${RED}✗ GET запрос неуспешен${NC}"
        return 1
    fi
    
    if ! unzip -o "$RESPONSE_ZIP"; then
        cd "$current_dir"
        rm -rf "$tmp_dir"
        echo -e "${RED}✗ Ошибка распаковки архива${NC}"
        return 1
    fi
    
    if [ -f "data.csv" ]; then
        echo -e "${GREEN}✓ GET запрос успешен${NC}"
        cd "$current_dir"
        rm -rf "$tmp_dir"
    else
        cd "$current_dir"
        rm -rf "$tmp_dir"
        echo -e "${RED}✗ GET запрос вернул некорректный архив${NC}"
        return 1
    fi
    
    return 0
}

check_api_advanced() {
    create_test_files 2
    
    echo -e "\nПроверка API (продвинутый уровень)"
    
    # Проверка POST с ZIP
    echo "Тестирование POST /api/v0/prices?type=zip"
    response=$(curl -s -F "file=@$TEST_ZIP" "${API_HOST}/api/v0/prices?type=zip")
    if [[ $response == *"total_items"* ]]; then
        echo -e "${GREEN}✓ POST запрос с ZIP успешен${NC}"
    else
        echo -e "${RED}✗ POST запрос с ZIP неуспешен${NC}"
        return 1
    fi
    
    # Проверка POST с TAR
    echo "Тестирование POST /api/v0/prices?type=tar"
    response=$(curl -s -F "file=@$TEST_TAR" "${API_HOST}/api/v0/prices?type=tar")
    if [[ $response == *"total_items"* ]]; then
        echo -e "${GREEN}✓ POST запрос с TAR успешен${NC}"
    else
        echo -e "${RED}✗ POST запрос с TAR неуспешен${NC}"
        return 1
    fi
    
    # Проверка GET
    check_api_simple
}

check_api_complex() {
    create_test_files 3
    echo -e "\nПроверка API (сложный уровень)"
    
    # Проверка POST с проверкой обработки некорректных данных
    echo "Тестирование POST /api/v0/prices?type=zip с некорректными данными"
    response=$(curl -s -F "file=@$TEST_ZIP" "${API_HOST}/api/v0/prices?type=zip")
    
    # Проверяем все обязательные поля в ответе
    local required_fields=("total_count" "duplicates_count" "total_items" "total_categories" "total_price")
    local missing_fields=()
    
    for field in "${required_fields[@]}"; do
        if [[ ! $response == *"\"$field\":"* ]]; then
            missing_fields+=($field)
        fi
    done
    
    if [ ${#missing_fields[@]} -eq 0 ]; then
        echo -e "${GREEN}✓ Все обязательные поля присутствуют в ответе${NC}"
    else
        echo -e "${RED}✗ Отсутствуют обязательные поля: ${missing_fields[*]}${NC}"
        return 1
    fi
    
    # Проверка корректности обработки некорректных данных
    total_count=$(echo $response | grep -o '"total_count":[0-9]*' | cut -d':' -f2)
    total_items=$(echo $response | grep -o '"total_items":[0-9]*' | cut -d':' -f2)
    
    if [ $total_count -gt $total_items ]; then
        echo -e "${GREEN}✓ Некорректные данные правильно отфильтрованы (total_count > total_items)${NC}"
    else
        echo -e "${RED}✗ Проблема с обработкой некорректных данных${NC}"
        return 1
    fi
    
    # Проверка обработки дубликатов
    duplicates_count=$(echo $response | grep -o '"duplicates_count":[0-9]*' | cut -d':' -f2)
    if [ $duplicates_count -gt 0 ]; then
        echo -e "${GREEN}✓ Дубликаты успешно обнаружены${NC}"
    else
        echo -e "${RED}✗ Проблема с обнаружением дубликатов${NC}"
        return 1
    fi

    # Проверка GET с фильтрами
    echo "Тестирование GET /api/v0/prices с фильтрами"
    filters="start=2024-01-01&end=2024-01-31&min=30&max=1000"
    
    # Сохраняем текущую директорию
    current_dir=$(pwd)
    
    # Создаем временную директорию и переходим в неё
    tmp_dir=$(mktemp -d)
    cd "$tmp_dir"
    
    if ! curl -s "${API_HOST}/api/v0/prices?${filters}" -o "$RESPONSE_ZIP"; then
        cd "$current_dir"
        rm -rf "$tmp_dir"
        echo -e "${RED}✗ GET запрос с фильтрами неуспешен${NC}"
        return 1
    fi
    
    if ! unzip -o "$RESPONSE_ZIP"; then
        cd "$current_dir"
        rm -rf "$tmp_dir"
        echo -e "${RED}✗ Ошибка распаковки архива${NC}"
        return 1
    fi
    
    if [ ! -f "data.csv" ]; then
        cd "$current_dir"
        rm -rf "$tmp_dir"
        echo -e "${RED}✗ Файл data.csv не найден в архиве${NC}"
        return 1
    fi
    
    # Проверяем, что в выгруженном файле нет некорректных данных
    invalid_lines=$(grep -E ",invalid_|^[^,]*,[^,]*,,[^,]*,|^[^,]*,,[^,]*,[^,]*," data.csv || true)
    if [ -z "$invalid_lines" ]; then
        echo -e "${GREEN}✓ Выгруженные данные не содержат некорректных записей${NC}"
    else
        cd "$current_dir"
        rm -rf "$tmp_dir"
        echo -e "${RED}✗ Обнаружены некорректные записи в выгрузке${NC}"
        return 1
    fi
    
    # Возвращаемся в исходную директорию и удаляем временную
    cd "$current_dir"
    rm -rf "$tmp_dir"
    
    return 0
}

check_postgres() {
    local level=$1
    
    echo -e "\nПроверка PostgreSQL (Уровень $level)"
    
    # Базовая проверка подключения для всех уровней
    if ! PGPASSWORD=$DB_PASSWORD psql -h $DB_HOST -U $DB_USER -d $DB_NAME -c '\q' 2>/dev/null; then
        echo -e "${RED}✗ PostgreSQL недоступен${NC}"
        return 1
    fi
    
    case $level in
        1)  
            echo "Выполняем проверку уровня 1"
            if PGPASSWORD=$DB_PASSWORD psql -h $DB_HOST -U $DB_USER -d $DB_NAME -c "
                SELECT COUNT(*) FROM prices;" 2>/dev/null; then
                echo -e "${GREEN}✓ PostgreSQL работает корректно${NC}"
                return 0
            else
                echo -e "${RED}✗ Ошибка выполнения запроса${NC}"
                return 1
            fi
            ;;
            
        2)  
            echo "Выполняем проверку уровня 2"
            if PGPASSWORD=$DB_PASSWORD psql -h $DB_HOST -U $DB_USER -d $DB_NAME -c "
                SELECT 
                    COUNT(*) as total_items,
                    COUNT(DISTINCT category) as total_categories,
                    SUM(price) as total_price
                FROM prices;" 2>/dev/null; then
                echo -e "${GREEN}✓ PostgreSQL работает корректно${NC}"
                return 0
            else
                echo -e "${RED}✗ Ошибка выполнения запроса${NC}"
                return 1
            fi
            ;;
            
        3)  
            echo "Выполняем проверку уровня 3"
            if PGPASSWORD=$DB_PASSWORD psql -h $DB_HOST -U $DB_USER -d $DB_NAME -c "
                WITH stats AS (
                    SELECT 
                        COUNT(*) as total_items,
                        COUNT(DISTINCT category) as total_categories,
                        SUM(price) as total_price,
                        COUNT(*) - COUNT(DISTINCT (name, category, price)) as duplicates
                    FROM prices
                    WHERE create_date BETWEEN '2024-01-01' AND '2024-01-31'
                    AND price BETWEEN 300 AND 1000
                )
                SELECT * FROM stats;" 2>/dev/null; then
                echo -e "${GREEN}✓ PostgreSQL работает корректно${NC}"
                return 0
            else
                echo -e "${RED}✗ Ошибка выполнения запроса${NC}"
                return 1
            fi
            ;;
        *)
            echo "Неизвестный уровень: $level"
            return 1
            ;;
    esac
    
    echo -e "${RED}✗ Проблема с PostgreSQL${NC}"
    return 1
}

cleanup() {
    rm -f $TEST_CSV $TEST_ZIP $TEST_TAR $RESPONSE_ZIP
}

main() {
    local level=$1
    local failed=0
    
    case $level in
        1)
            echo "=== Запуск проверки простого уровня ==="
            check_api_simple
            failed=$((failed + $?))
            check_postgres 1
            failed=$((failed + $?))
            ;;
        2)
            echo "=== Запуск проверки продвинутого уровня ==="
            check_api_advanced
            failed=$((failed + $?))
            check_postgres 2
            failed=$((failed + $?))
            ;;
        3)
            echo "=== Запуск проверки сложного уровня ==="
            check_api_complex
            failed=$((failed + $?))
            check_postgres 3
            failed=$((failed + $?))
            ;;
        *)
            echo "Неверный уровень проверки"
            cleanup
            exit 1
            ;;
    esac
    
    cleanup
    
    echo -e "\nИтоги проверки:"
    if [ $failed -eq 0 ]; then
        echo -e "${GREEN}✓ Все проверки пройдены успешно${NC}"
        exit 0
    else
        echo -e "${RED}✗ Обнаружены проблемы в $failed проверках${NC}"
        exit 1
    fi
}

# Проверка аргументов
if [ $# -ne 1 ] || ! [[ $1 =~ ^[1-3]$ ]]; then
    echo "Использование: $0 <уровень_проверки>"
    echo "Уровень проверки должен быть:"
    echo "  1 - простой уровень"
    echo "  2 - продвинутый уровень"
    echo "  3 - сложный уровень"
    exit 1
fi

main "$1"
