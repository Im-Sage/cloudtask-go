class LRUCache {
private:
    // 双向链表节点
    struct DlinkedNode {
        int key; // 淘汰节点时，需要用到key从哈希表中删除该节点
        int value;
        DlinkedNode* prev;
        DlinkedNode* next;

        // 两种构造函数
        DlinkedNode() : key(0), value(0), prev(nullptr), next(nullptr) {}

        DlinkedNode(int key, int value)
            : key(key), value(value), prev(nullptr), next(nullptr) {}
    };

    // key -> 链表节点地址
    unordered_map<int, DlinkedNode*> cache;

    // 当前缓存中的实际节点数
    int size;

    // 最大容量
    int capacity;

    // 虚拟头结点和虚拟尾节点
    DlinkedNode* head;
    DlinkedNode* tail;

    // 将节点添加到链表头部
    void addToHead(DlinkedNode* node) { // node为要加入的节点
        // 灵魂四步
        node->next = head->next;
        head->next->prev = node;
        node->prev = head;
        head->next = node;
    }

    // 从链表删除指定节点,在链表的任意位置完成
    void removeNode(DlinkedNode* node) {
        node->next->prev = node->prev;
        node->prev->next = node->next;
    }

    // 将指定节点移动到链表头部
    void moveToHead(DlinkedNode* node){
        // 先删除节点
        removeNode(node);
        // 再移到链头 
        addToHead(node);
    }

    // 删除并返回尾部的节点
    DlinkedNode* removeTail() {
        DlinkedNode* node = tail->prev;
        removeNode(node);
        return node;
    }

public:
    LRUCache(int capacity) { 
        this->capacity = capacity;
        this->size = 0;

        head = new DlinkedNode();
        tail = new DlinkedNode();

        // 初始化空链表
        head -> next = tail;
        tail -> prev = head;
         }

    // 找到就返回对应的值，并标记为最近使用，找不到返回-1
    int get(int key) {
        if(cache.find(key) == cache.end()){
            // 如果关键字不存在
            return -1;
        }
        DlinkedNode* node = cache[key];
        // 移到链表头部
        moveToHead(node);
        return node->value;
    }

    // 存入或更新数据，并把它标记为最近使用。存入数据时，如果容量已满，淘汰最久未使用的数据
    void put(int key, int value) {
        // 如果关键字不存在,不存在则必将插入
        if(cache.find(key) == cache.end()){
            DlinkedNode* node = new DlinkedNode(key, value);
            cache[key] = node;  // 在哈希表中建立映射
            // 原先不存在则必先插入，即先插入再说
            addToHead(node);
            size++;
            // 插入之后可能会超容量,则删除链尾节点
            if(size > capacity){
                DlinkedNode* willDeleteNode = removeTail();
                cache.erase(willDeleteNode->key);
                delete willDeleteNode;
                size--;
            }
        }else{ // 链表中本身就存在时，移动即可,要更新值
            DlinkedNode* node = cache[key];
            node -> value = value;
            moveToHead(node);
        }
        
    }
};

/**
 * Your LRUCache object will be instantiated and called as such:
 * LRUCache* obj = new LRUCache(capacity);
 * int param_1 = obj->get(key);
 * obj->put(key,value);
 */