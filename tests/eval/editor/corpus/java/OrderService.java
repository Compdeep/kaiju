package com.example.orders;

import java.util.List;
import java.util.Optional;

public class OrderService {

    private static final int MAX_ITEMS = 50;
    private final OrderRepository repository;
    private final PricingClient pricing;

    public OrderService(OrderRepository repository, PricingClient pricing) {
        this.repository = repository;
        this.pricing = pricing;
    }

    public Optional<Order> findById(long id) {
        return repository.findById(id);
    }

    public Order create(List<OrderLine> lines) {
        if (lines.size() > MAX_ITEMS) {
            throw new IllegalArgumentException("too many items");
        }
        long total = pricing.total(lines);
        Order order = new Order(lines, total);
        return repository.save(order);
    }

    public void cancel(long id) {
        repository.findById(id).ifPresent(order -> {
            order.setStatus(OrderStatus.CANCELLED);
            repository.save(order);
        });
    }
}
